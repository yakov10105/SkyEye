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

	"gorm.io/gorm"

	"SkyEye/internal/cache"
	"SkyEye/internal/db"
	"SkyEye/internal/messaging"
	"SkyEye/internal/telemetry"
)

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
	defer shutdownTracer(context.Background())

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

	// --- Health Checks ---
	go startHealthCheckServer(cfg.ListenAddress, dbClient, cacheClient, mqClient)

	// --- Scheduler ---
	scheduler := NewJobScheduler(cfg, cacheClient, dbClient, mqClient, logger)
	if err := scheduler.Start(); err != nil {
		slog.Error("failed to start scheduler", "error", err)
		os.Exit(1)
	}

	// --- Graceful Shutdown ---
	waitForShutdown(scheduler)
	slog.Info("orchestrator shut down gracefully")
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

func waitForShutdown(s *JobScheduler) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop

	slog.Info("shutting down orchestrator...")
	s.Stop()
}