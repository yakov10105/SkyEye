package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/viper"
)

const (
	leaderKey     = "orchestrator:leader"
	lockTTL       = 30 * time.Second
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
	Schedule      string   `mapstructure:"SCHEDULE"`
}

// loadConfig initializes and loads the service configuration.
func loadConfig() (*Config, error) {
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("REDIS_ADDR", "localhost:6379")
	viper.SetDefault("DB_PRIMARY_DSN", "host=localhost user=postgres password=postgres dbname=skyeye port=5432 sslmode=disable")
	viper.SetDefault("RABBITMQ_URI", "amqp://guest:guest@localhost:5672/")
	viper.SetDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	viper.SetDefault("JOB_QUEUE_NAME", "scrape_jobs")
	viper.SetDefault("LISTEN_ADDRESS", ":8080")
	viper.SetDefault("SCHEDULE", "10s")

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

// parseLogLevel parses the log level string into a slog.Level.
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
