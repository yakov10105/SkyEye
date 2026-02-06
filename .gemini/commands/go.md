🚀 Lead Go Engineer Instructions: Project "SkyEye"

1. Architectural Boundaries (The SkyEye Monorepo)
   We follow a Clean Architecture within a Go Workspace (go.work) to maintain strict separation between the scraping engine and data persistence.

- **/api/v1**: Protobuf definitions. This is the Source of Truth for internal gRPC communication.
- **/internal (Shared Library)**:
  - **/internal/messaging**: High-level RabbitMQ wrappers (connection pooling, automated re-dial, and Quorum Queue logic).
  - **/internal/cache**: Redis logic. Must contain the Lua scripts for atomic rate limiting and Redlock for job distributed locking.
  - **/internal/db**: GORM + dbresolver configuration for routing to Postgres Read Replicas.
- **/services**:
  - **/services/orchestrator**: Job scheduling and Leader Election.
  - **/services/scraper**: The high-concurrency engine (Core logic: Worker Pools).
  - **/services/ingest**: Persistence and data normalization.

2. Configuration

- **Centralized Management**: Use `viper` for configuration. Load configuration from a default file (e.g., `config.yaml`) and override with environment variables. This provides flexibility for both development and production.
- **Strict Loading**: The application must fail on startup if required configuration variables are missing.

3. Distributed Idiomatic Go

- **Context Propagation**: Every function must accept `context.Context`. This context must carry the OpenTelemetry TraceID across service boundaries (injected into RabbitMQ headers).
- **Graceful Shutdown**: Every service must listen for `SIGTERM` and `SIGINT`.
  - **Scraper**: Must stop consuming from RabbitMQ, finish active Goroutines, and Ack messages before exiting.
- **Error Wrapping**: Use `%w` for wrapping. Differentiate between transient errors (retryable) and permanent errors (send to Dead Letter Exchange).

4. High-Scale Concurrency Patterns

- **Worker Pools (The Semaphore Pattern)**:
  ```go
  // Limit active scrapers to 1000 to prevent OOM
  sem := make(chan struct{}, 1000)
  for job := range jobsChan {
      sem <- struct{}{}
      go func(j Job) {
          defer func() { <-sem }()
          process(j)
      }(job)
  }
  ```
- **ErrGroup for Parallelism**: Use `golang.org/x/sync/errgroup` for managing sub-tasks (e.g., fetching a page while simultaneously querying a cache).
- **Non-Blocking Redis Calls**: All Redis interactions for rate limiting must have a strict timeout (e.g., 50ms) to ensure the scraper doesn't hang on a cache delay.

5. Performance & Memory Management

- **Pre-allocation**: Always `make([]T, 0, expectedSize)` when parsing scraped data to avoid slice growth in hot paths.
- **Buffer Recycling**: Use `sync.Pool` for `bytes.Buffer` and `json.Decoder` objects used during HTML parsing and result serialization.
- **Protobuf over JSON**: All internal communication (Orchestrator → RabbitMQ → Scraper) must use Protocol Buffers to minimize CPU overhead and bandwidth.

6. Persistence & Messaging Standards

- **Postgres Read/Write Splitting**:
  - Scheduler queries Read Replicas only.
  - Ingest writes to Primary.
  - Use `db.WithContext(ctx).Clauses(dbresolver.Write/Read)` explicitly when intent is ambiguous.
- **RabbitMQ Quorum Queues**: Code must assume at-least-once delivery. Use Idempotency Keys (Job ID) stored in Redis to prevent duplicate processing.
- **Circuit Breakers**: Wrap external HTTP scrapers in `sony/gobreaker`. If a target site returns 5xx errors, the breaker must trip immediately.

7. Observability & Logging

- **Structured Logging**: Use the standard library `slog` with a `slog.JSONHandler`. All log entries must be in JSON format.
- **TraceID Injection**: The OpenTelemetry `TraceID` from the context must be automatically included in every log entry for unified observability.
- **Observability Assertions**: Ensure OpenTelemetry spans are correctly started and finished in tests.

8. Cloud-Native Patterns

- **Health Checks**: Every service must expose two HTTP endpoints:
  - `GET /livez`: Liveness probe. Returns `200 OK` if the service is running.
  - `GET /readyz`: Readiness probe. Returns `200 OK` if the service is ready to accept traffic (e.g., database connections are up, dependent services are available).

9. Testing & Reliability

- **The Race Flag**: All tests MUST pass with `go test -race ./...`.
- **Integration (Dockertest)**: No mock-only testing for RabbitMQ or Redis. Use `dockertest` to spin up a real RabbitMQ cluster and Redis instance for "Worker" unit tests.

10. Code Documentation

- **Godoc Standard**: All exported identifiers (functions, types, constants, variables) must have clear, `godoc`-compatible comments.
- **Clarity Over Brevity**: Comments should explain the _what_ and the _why_, not the _how_. The code itself shows how it works.

  ```go
  // Good
  // AddItem adds a new item to the cache with a default expiration.
  // It returns an error if the item already exists to prevent race conditions.
  func (c *Cache) AddItem(item *Item) error { ... }

  // Bad
  // AddItem iterates over the items and adds the new one
  func (c *Cache) AddItem(item *Item) error { ... }
  ```

11. Security

- **Input Validation**: All incoming data from external sources (API requests, message payloads) must be strictly validated before processing.
- **Secret Management**: Secrets must never be logged or exposed. Use a library like `viper` to read them from environment variables injected by the orchestration layer (e.g., Kubernetes Secrets).

12. Prohibited Practices

- ❌ **No Local Caching**: Do not use `map` for rate limiting or any shared state. Use Redis. Pods are ephemeral and scaled by KEDA.
- ❌ **No Naked Goroutines**: Never start a goroutine without a `WaitGroup`, `errgroup`, or a mechanism for recovery/timeout.
- ❌ **No Un-vetted Dependencies**: Do not add a new third-party dependency without team review. Prioritize the standard library and well-known, community-trusted libraries.
