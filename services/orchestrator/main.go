package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/go-redsync/redsync/v4"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"gorm.io/plugin/dbresolver"
	"google.golang.org/protobuf/proto"

	"SkyEye/api/v1"
	"SkyEye/internal/cache"
	"SkyEye/internal/db"
	"SkyEye/internal/messaging"
	"SkyEye/internal/telemetry"
)


const (
	leaderKey     = "orchestrator:leader"
	lockTTL       = 30 * time.Second
	schedule      = "10s"
	jobsBatchSize = 500
)

// Config holds all configuration for the service.
type Config struct {
	LogLevel      string   `mapstructure:"LOG_LEVEL"`
	RedisAddr     string   `mapstructure:"REDIS_ADDR"`
	PrimaryDSN    string   `mapstructure:"DB_PRIMARY_DSN"`
	ReadReplicas  []string `mapstructure:"DB_READ_REPLICAS"`
	RabbitMQURI   string   `mapstructure:"RABBITMQ_URI"`
	OtelExporter  string   `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	JobQueueName  string   `mapstructure:"JOB_QUEUE_NAME"`
	ListenAddress string   `mapstructure:"LISTEN_ADDRESS"`
}

func main() {
	// --- Configuration ---
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// --- Logging ---
	lvl := parseLogLevel(cfg.LogLevel)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(logger)

	// --- Telemetry ---
	shutdownTracer, err := telemetry.SetupTracer(context.Background(), "skyeye-orchestrator", cfg.OtelExporter)
	if err != nil {
		slog.Error("failed to setup tracer", "error", err)
		os.Exit(1)
	}
	defer shutdownTracer()

	// --- Service Dependencies ---
	slog.Info("initializing service dependencies")
	cacheClient, err := cache.NewClient(context.Background(), cfg.RedisAddr)
	if err != nil {
		slog.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	defer cacheClient.Close()

	dbClient, err := db.New(context.Background(), db.Config{PrimaryDSN: cfg.PrimaryDSN, ReadReplicas: cfg.ReadReplicas})
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	mqClient, err := messaging.NewConnection(cfg.RabbitMQURI)
	if err != nil {
		slog.Error("failed to connect to rabbitmq", "error", err)
		os.Exit(1)
	}
	defer mqClient.Close()

	instanceID := uuid.New().String()
	slog.Info("starting orchestrator", "instanceID", instanceID, "schedule", schedule)

	// --- Health Checks ---
	go startHealthCheckServer(cfg.ListenAddress, dbClient, cacheClient, mqClient)

	// --- Scheduler ---
	s := gocron.NewScheduler(time.UTC)
	s.SetMaxConcurrentJobs(1, gocron.DiscardMode)

	_, err = s.Every(schedule).Do(func() {
		runJob(context.Background(), cacheClient, dbClient, mqClient, cfg.JobQueueName, instanceID)
	})
	if err != nil {
		slog.Error("failed to schedule job", "error", err)
		os.Exit(1)
	}
	s.StartAsync()

	// --- Graceful Shutdown ---
	waitForShutdown(s)
	slog.Info("orchestrator shut down gracefully")
}

// runJob is the main function executed by the scheduler. It attempts to gain
// leadership and, if successful, dispatches jobs.
func runJob(ctx context.Context, cacheClient *cache.Client, dbClient *gorm.DB, mqClient *messaging.Connection, queueName, instanceID string) {
	slog.Debug("running scheduled job", "instanceID", instanceID)

	// Try to acquire leadership lock
	mutex, err := cacheClient.Lock(ctx, leaderKey, lockTTL)
	if err != nil {
		// If we fail to acquire the lock, it's likely another instance is the leader.
		if errors.Is(err, redsync.ErrFailed) {
			slog.Debug("could not acquire lock, another instance is leader", "instanceID", instanceID)
		} else {
			slog.Error("failed to acquire lock", "error", err)
		}
		return
	}
	defer cacheClient.Unlock(ctx, mutex)

	slog.Info("acquired leadership, dispatching jobs", "instanceID", instanceID)
	dispatchJobs(ctx, dbClient, mqClient, queueName)
}

// dispatchJobs fetches due jobs from the database and publishes them to the message queue.
func dispatchJobs(ctx context.Context, db *gorm.DB, mq *messaging.Connection, queueName string) {
	// This struct represents the job model stored in the database.
	type Job struct {
		ID  uint   `gorm:"primaryKey"`
		URL string `gorm:"column:url"`
		// other fields like status, etc.
	}

	slog.Info("fetching jobs from database...", "batch_size", jobsBatchSize)

	// TODO: Replace with a more sophisticated query, possibly using "FOR UPDATE SKIP LOCKED".
	var jobs []Job
	// Using Read resolver for this query
	result := db.WithContext(ctx).Clauses(dbresolver.Read).Where("status = ?", "pending").Limit(jobsBatchSize).Find(&jobs)
	if result.Error != nil {
		slog.Error("failed to fetch jobs from database", "error", result.Error)
		return
	}

	if len(jobs) == 0 {
		slog.Info("no pending jobs found")
		return
	}

	slog.Info("found jobs to dispatch", "count", len(jobs))
	for _, job := range jobs {
		scrapeJob := &v1.ScrapeJob{
			JobId:         fmt.Sprintf("%d", job.ID),
			Url:           job.URL,
			MaxRetries:    3, // This could be a field in the DB job model
			CorrelationId: uuid.New().String(),
		}

		payload, err := proto.Marshal(scrapeJob)
		if err != nil {
			slog.Error("failed to marshal scrape job", "jobID", job.ID, "error", err)
			continue
		}

		err = mq.Publish(ctx, "", queueName, payload) // Default exchange
		if err != nil {
			slog.Error("failed to publish job to message queue", "jobID", job.ID, "error", err)
			// TODO: Add retry logic or mark job as failed, for now we just log
			continue
		}

		// TODO: Update job status to "queued" in the database.
		// Example: db.Model(&job).Update("status", "queued")
		slog.Debug("dispatched job", "jobID", job.ID, "correlationID", scrapeJob.CorrelationId)
	}
	slog.Info("finished dispatching jobs", "count", len(jobs))
}

// --- Helper Functions ---

func loadConfig() (*Config, error) {
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("REDIS_ADDR", "localhost:6379")
	viper.SetDefault("DB_PRIMARY_DSN", "host=localhost user=postgres password=postgres dbname=skyeye port=5432 sslmode=disable")
	viper.SetDefault("RABBITMQ_URI", "amqp://guest:guest@localhost:5672/")
	viper.SetDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	viper.SetDefault("JOB_QUEUE_NAME", "scrape_jobs")
	viper.SetDefault("LISTEN_ADDRESS", ":8080")

	viper.AutomaticEnv()
	viper.AllowEmptyEnv(true)

	var cfg Config
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	slog.Info("configuration loaded successfully")
	return &cfg, nil
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func startHealthCheckServer(addr string, dbClient *gorm.DB, cacheClient *cache.Client, mqClient *messaging.Connection) {
	http.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	http.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Check DB connection
		sqlDB, err := dbClient.DB()
		if err != nil {
			http.Error(w, "failed to get db instance", http.StatusInternalServerError)
			return
		}
		if err := sqlDB.PingContext(r.Context()); err != nil {
			slog.Error("readiness probe failed: db ping", "error", err)
			http.Error(w, "db ping failed", http.StatusInternalServerError)
			return
		}

		// Check Redis connection
		if err := cacheClient.Ping(r.Context()); err != nil {
			slog.Error("readiness probe failed: redis ping", "error", err)
			http.Error(w, "redis ping failed", http.StatusInternalServerError)
			return
		}

		// Check RabbitMQ connection
		if mqClient.IsClosed() {
			slog.Error("readiness probe failed: rabbitmq connection closed")
			http.Error(w, "rabbitmq connection is closed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	slog.Info("starting health check server", "address", addr)
	if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("health check server failed", "error", err)
	}
}

func waitForShutdown(s *gocron.Scheduler) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop

	slog.Info("shutting down orchestrator...")
	s.Stop()
}
