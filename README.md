# SkyEye Project

SkyEye is a high-performance, distributed web scraping system designed for scalability and resilience. It uses a microservices architecture to separate concerns, from job scheduling and scraping to data persistence.

## Architecture

The system is composed of several microservices that communicate asynchronously via a message queue. This decoupled design allows for independent scaling and development of each component.

Key architectural patterns include:
- **Event-Driven:** Services communicate through RabbitMQ, allowing for scalable, asynchronous processing.
- **Distributed Locking:** Redis is used for leader election in the Orchestrator service to ensure only one instance schedules jobs at a time.
- **Read/Write Splitting:** The system uses a primary PostgreSQL database for writes and read replicas for read-intensive operations to optimize database performance.
- **Event-Driven Autoscaling:** KEDA is used to automatically scale the number of Scraper pods based on the RabbitMQ queue length.

```mermaid
graph TD
    subgraph "Kubernetes Cluster"
        direction LR

        subgraph "SkyEye Services"
            Orchestrator -- "Publishes Jobs" --> RabbitMQ;
            Scraper -- "Consumes Jobs" --> RabbitMQ;
            Scraper -- "Rate Limits & Locks" --> Redis;
            Scraper -- "Publishes Results" --> RabbitMQ_Results[RabbitMQ];
            Ingest -- "Consumes Results" --> RabbitMQ_Results;
            Ingest -- "Writes Data" --> Postgres_Primary[Postgres Primary];
            Orchestrator -- "Reads Tasks" --> Postgres_Replica[Postgres Replica];
        end

        subgraph "Infrastructure"
            RabbitMQ
            Redis
            Postgres_Primary
            Postgres_Replica
        end

        KEDA -- "Monitors Queue" --> RabbitMQ;
        KEDA -- "Scales" --> Scraper;
    end

    User -- "Manages Deployments" --> GitOps[GitOps Repo (ArgoCD)];
    GitOps -- "Syncs" --> Kubernetes_Cluster[Kubernetes Cluster];
```

## Services

- **Orchestrator:** The brain of the system. It periodically scans the database for due scraping tasks, performs leader election to ensure single-instance execution, and publishes job messages to a RabbitMQ queue.
- **Scraper:** The workhorse. This service consumes jobs from RabbitMQ, uses a worker pool for high-concurrency scraping, respects rate limits via Redis, and publishes the scraped data back to another queue.
- **Ingest:** The persistence layer. It consumes scraped data from RabbitMQ and saves it to the PostgreSQL database, separating the data storage logic from the scraping process.

## Repository Structure

The project is organized into two main repositories: one for the application code (`skyeye`) and another for the GitOps configuration (`skyeye-gitops`).

```plaintext
skyeye/
├── go.work
├── api/v1/
│   └── scraper.proto
├── internal/
│   ├── messaging/
│   ├── cache/
│   ├── db/
│   └── telemetry/
└── services/
    ├── orchestrator/
    ├── scraper/
    └── ingest/

skyeye-gitops/
├── apps/
├── base/
├── envs/
└── infrastructure/
```

## Getting Started

*(Placeholder for build, run, and deployment instructions.)*
