# SkyEye Product Requirements Document (PRD)

## 1. Project Topology & Repository Structure

We will use a Go Workspace (Monorepo). This allows us to share protobuf definitions and internal libraries across all services while keeping them independently deployable.

**Directory Structure:**

```plaintext
skyeye/
├── go.work                # The "glue" for Go microservices
├── api/                   # Contracts (Protobuf/gRPC)
│   └── v1/
│       └── scraper.proto  # Shared interface definitions
├── internal/              # Private, shared Go packages
│   ├── messaging/         # RabbitMQ wrapper (Retries, DLX logic)
│   ├── cache/             # Redis logic (Redlock, Lua scripts)
│   ├── db/                # GORM setup + Read Replica logic
│   └── telemetry/         # OpenTelemetry (Otel) setup
├── services/              # The actual microservices
│   ├── orchestrator/      # Service A: Main entry, Dockerfile
│   ├── scraper/           # Service B: Main entry, Dockerfile
│   └── ingest/            # Service C: Main entry, Dockerfile
├── pkg/                   # Publicly shareable code (Optional)
└── Makefile               # Task runner (build, test, protoc)


skyeye-gitops/
├── apps/                  # ArgoCD "Application" definitions
│   ├── staging.yaml
│   └── prod.yaml
├── base/                  # Core Helm charts or K8s manifests
│   └── skyeye-app/        # Templates for deployments, services, HPA
├── envs/                  # Environment-specific overrides
│   ├── staging/           # Lower replicas, small DBs
│   │   └── values.yaml
│   └── prod/              # High replicas, KEDA config, multi-AZ DBs
│       └── values.yaml
└── infrastructure/        # 3rd party infra (Bitnami charts)
    ├── postgres-values.yaml
    ├── rabbitmq-values.yaml
    └── redis-values.yaml
```

---

## 2. Microservice "Deep-Dive" Implementation

### A. Service 1: The Orchestrator (Job Dispatcher)

- **What it does:** Scans the database for due tasks and pushes them to RabbitMQ.
- **Why we need it:** We need a centralized point to manage scheduling without overloading the scrapers.
- **Technologies:** Go-Cron, Go-Redis (for Leader Election).
- **Implementation Details:**
  - **Leader Election:** In K8s, you’ll have 3 replicas of this service. We use Redis Distributed Locking (`setnx`) to ensure only one instance is active. If the leader pod dies, another pod acquires the lock and takes over.
  - **Batching:** Instead of one-by-one, it pulls 500 records from Postgres Read Replicas using a "Skip Locked" strategy to avoid database contention.

### B. Service 2: The Scraper Engine (The Muscle)

This is where your Go concurrency skills shine.

- **What it does:** Consumes jobs, respects rate limits via Redis, and executes the scrape.
- **Technologies:** colly/v2 (scraping), amqp091-go (RabbitMQ), Redlock-go.
- **Implementation Details:**
  - **Worker Pool:** We use a `chan struct{}` as a Semaphore to limit the number of active Goroutines to, say, 1,000.
  - **Distributed Rate Limiting:** Before fetching a URL, the service calls a Redis Lua Script.
  - **Why Lua?** It ensures the "Check-then-Decrement" operation is Atomic.
  - **Circuit Breaking:** We use `sony/gobreaker`. If a specific target site is down, the breaker "trips" and we stop wasting RabbitMQ messages on it for 60 seconds.

### C. Service 3: The Data Ingest (Persistence)

- **What it does:** Consumes scraped JSON and saves it to Postgres.
- **Why we need it:** To separate the "heavy" scraping logic from the "critical" data storage logic.
- **Technologies:** GORM with dbresolver.
- **Implementation Details:**
  - **Read-Write Splitting:** We use the `dbresolver` plugin.
  - `db.Create(&result)` → Automatically goes to Postgres Primary.
  - `db.Find(&results)` → Automatically load-balances across Postgres Read Replicas.

---

## 3. The Infrastructure Stack (Production-Ready)

### Persistence: PostgreSQL (Bitnami Chart)

- **Configuration:** 1 Primary, 2 Read Replicas.
- **Implementation:** Use Streaming Replication.
- **Why:** High-scale systems fail if the DB can't handle reads. Moving analytics/status checks to replicas keeps the Primary fast for writes.

### Messaging: RabbitMQ (Cluster Mode)

- **Configuration:** 3-node cluster with Quorum Queues.
- **Why:** Quorum queues use the Raft consensus algorithm. Even if a K8s node catches fire, your messages (and the scraping jobs) are never lost.

### Observability: OpenTelemetry (OTel)

- **Implementation:** Use the `otelgrpc` and `otelhttp` instrumentation.
- **Trace Propagation:** When the Orchestrator sends a message to RabbitMQ, it injects a `traceparent` header. The Scraper extracts it.
- **Result:** In Grafana Tempo, you can see a single timeline: "Job Created" → "Queue Wait Time" → "Scrape Execution" → "DB Save."

---

## 4. Scaling on Kubernetes (KEDA)

Standard CPU/Memory scaling is useless here. We need Event-Driven Scaling.

- **Implementation:** Install KEDA (Kubernetes Event-driven Autoscaling).
- **The Logic:**
  - KEDA monitors the RabbitMQ queue length.
  - If `queue_length > 1000`, KEDA tells K8s to spin up 50 Scraper pods.
  - If `queue_length == 0`, KEDA scales the pods down to zero to save cloud costs.

---

## 5. Deployment Repo: The GitOps Way

We use ArgoCD for the "Single Source of Truth."

1. **CI (GitHub Actions):** Build Docker image → Run `go test -race` → Push to Registry.
2. **GitOps Sync:** The CI script updates a `values.yaml` file in the Deployment Repo.
3. **ArgoCD:** Detects the change and performs a Blue-Green Deployment in your K8s cluster. If the new version fails health checks, it automatically rolls back.

## Phase 1: Foundation & Repository Setup

This phase focuses on establishing the core project structure and contracts.

### ✅ Task 1.1: Initialize Go Monorepo (`skyeye`)

- **Description:** Set up the main `skyeye` Go workspace monorepo. This includes creating the directory structure as defined in the PRD.
- **Definition of Done:**
  - ✅ A `skyeye` directory exists.
  - ✅ A `go.work` file is present at the root.
  - ✅ The following directories are created: `api/v1`, `internal`, `services`, `pkg`.
  - ✅ A `Makefile` is created with placeholder targets for `build`, `test`, and `protoc`.

### ✅ Task 1.2: Initialize GitOps Repository (`skyeye-gitops`)

- **Description:** Set up the `skyeye-gitops` repository that will hold all Kubernetes and Helm configurations for ArgoCD.
- **Definition of Done:**
  - ✅ A `skyeye-gitops` directory exists.
  - ✅ The following directories are created: `apps`, `base/skyeye-app`, `envs/staging`, `envs/prod`, `infrastructure`.
  - ✅ Placeholder `.yaml` files are created as per the PRD structure (e.g., `apps/staging.yaml`).

### Task 1.3: Define Protobuf Contracts

- **Description:** Create the initial `scraper.proto` file. This defines the gRPC services and messages that microservices will use to communicate.
- **Definition of Done:**
  - The `api/v1/scraper.proto` file is created.
  - It contains initial message definitions for a scraping job and its result.
  - The `protoc` command in the `Makefile` can successfully generate Go code from the `.proto` file.

---

## Phase 2: Core Internal Libraries

This phase involves building the shared, private Go packages that will be used by all microservices.

### Task 2.1: Implement RabbitMQ Wrapper (`internal/messaging`)

- **Description:** Develop a Go package to handle interactions with RabbitMQ. It should abstract away the complexity of connection, retries, and dead-letter exchange (DLX) logic.
- **Definition of Done:**
  - Package provides functions to `Publish` a message and `Consume` messages from a specific queue.
  - Connection retry logic is implemented.
  - Unit tests are written to verify publishing and consuming, including failure scenarios.
  - The package is capable of injecting and extracting OpenTelemetry traceparent headers.

### Task 2.2: Implement Redis Cache Logic (`internal/cache`)

- **Description:** Develop a package for interacting with Redis, specifically for distributed locking and rate limiting.
- **Definition of Done:**
  - Implements a `Lock()` and `Unlock()` function using `setnx` for leader election.
  - Includes a function to execute a provided Lua script for atomic operations (for the rate limiter).
  - All functions are tested against a real Redis instance.

### Task 2.3: Implement GORM Database Logic (`internal/db`)

- **Description:** Create a package to manage database connections using GORM, including the read-replica configuration.
- **Definition of Done:**
  - The package initializes a GORM `*gorm.DB` object.
  - It correctly configures the `dbresolver` plugin for read/write splitting.
  - Provides a function to return the configured DB instance.
  - Tests verify that `Create` operations target the primary and `Find` operations target replicas.

### Task 2.4: Implement OpenTelemetry Wrapper (`internal/telemetry`)

- **Description:** Create a centralized package to configure and initialize OpenTelemetry for all services.
- **Definition of Done:**
  - The package provides a `SetupTracer` function.
  - It configures exporters for gRPC and HTTP instrumentation.
  - It can be easily integrated into each microservice's `main.go`.

---

## Phase 3: Microservice Development

This phase focuses on building the individual, deployable services.

### Task 3.1: Develop Orchestrator Service

- **Description:** Build the service responsible for scheduling scrape jobs.
- **Definition of Done:**
  - The service uses the `internal/cache` package to perform leader election. Only one replica is active.
  - It uses `go-cron` to periodically scan the database.
  - It fetches due tasks from a Postgres read replica using `internal/db`.
  - It publishes job messages to RabbitMQ using the `internal/messaging` package.
  - A `Dockerfile` is created for building the service image.
  - All logic is covered by unit and integration tests.

### Task 3.2: Develop Scraper Engine Service

- **Description:** Build the service that consumes jobs and performs the web scraping.
- **Definition of Done:**
  - The service consumes jobs from a RabbitMQ queue using `internal/messaging`.
  - It uses a worker pool (Semaphore) to limit concurrent scraping goroutines.
  - It implements a distributed rate limiter using the Redis Lua script from `internal/cache`.
  - It integrates `sony/gobreaker` for circuit breaking on failing target sites.
  - It uses `colly/v2` to perform the scraping.
  - Scraped data is published to another RabbitMQ queue for ingestion.
  - OpenTelemetry traces are correctly propagated from the incoming message.
  - A `Dockerfile` is created for the service.

### Task 3.3: Develop Data Ingest Service

- **Description:** Build the service that persists scraped data into the database.
- **Definition of Done:**
  - The service consumes scraped data from a RabbitMQ queue.
  - It uses the `internal/db` package with the `dbresolver` to write data to the Postgres primary.
  - Bulk insertion logic is implemented for efficiency if necessary.
  - A `Dockerfile` is created for the service.

---

## Phase 4: Infrastructure Deployment

This phase involves setting up the production-grade infrastructure on Kubernetes.

### Task 4.1: Deploy PostgreSQL

- **Description:** Deploy a production-ready PostgreSQL cluster using the Bitnami Helm chart.
- **Definition of Done:**
  - A `postgres-values.yaml` is created in the `infrastructure` directory of the `skyeye-gitops` repo.
  - The chart is configured for 1 primary and 2 read replicas with streaming replication.
  - The deployment is running and accessible within the Kubernetes cluster.

### Task 4.2: Deploy RabbitMQ

- **Description:** Deploy a 3-node RabbitMQ cluster using the official Helm chart.
- **Definition of Done:**
  - A `rabbitmq-values.yaml` is created in the `infrastructure` directory.
  - The chart is configured to deploy a 3-node cluster.
  - Quorum Queues are enabled by default.
  - The cluster is stable and the management UI is accessible.

### Task 4.3: Setup Observability Stack

- **Description:** Deploy OpenTelemetry collectors and Grafana Tempo for distributed tracing.
- **Definition of Done:**
  - An OpenTelemetry Collector is deployed in the cluster.
  - Grafana Tempo is deployed and configured with the OTel Collector as a data source.
  - Traces from the SkyEye services are successfully ingested and can be visualized in Grafana.

---

## Phase 5: Automation, Scaling, and GitOps

This final phase connects everything and automates the deployment and scaling processes.

### Task 5.1: Create GitHub Actions CI Pipeline

- **Description:** Create a CI pipeline that builds, tests, and publishes Docker images.
- **Definition of Done:**
  - A GitHub Actions workflow is created in the `skyeye` repository.
  - On every push to `main`, the workflow runs `go test -race`.
  - On tagged releases, it builds Docker images for all services.
  - The workflow automatically updates the image tag in the `values.yaml` file in the `skyeye-gitops` repository and commits the change.

### Task 5.2: Configure ArgoCD Applications

- **Description:** Configure ArgoCD to monitor the `skyeye-gitops` repository and deploy the applications.
- **Definition of Done:**
  - ArgoCD `Application` manifests are created in the `apps` directory (`staging.yaml`, `prod.yaml`).
  - ArgoCD is configured to point to the `skyeye-gitops` repository.
  - The `base/skyeye-app` Helm chart is created to template the deployments for all three microservices.
  - Environment-specific overrides for staging and prod are defined in their respective `values.yaml` files.
  - ArgoCD successfully syncs the application and deploys the services to the staging environment.

### Task 5.3: Implement KEDA Autoscaling

- **Description:** Configure KEDA to automatically scale the `scraper` service based on RabbitMQ queue length.
- **Definition of Done:**
  - KEDA is installed in the Kubernetes cluster.
  - A `ScaledObject` manifest is created for the scraper deployment.
  - The scaler is configured to monitor the scraper's job queue.
  - KEDA successfully scales the scraper pods up when the queue length exceeds the threshold and scales down to zero when the queue is empty.
  - A Blue-Green deployment strategy is configured in ArgoCD, with automated rollback on health check failures.
