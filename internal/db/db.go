package db

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

// Config holds the database connection strings.
type Config struct {
	PrimaryDSN   string
	ReadReplicas []string
}

// New creates a new database connection with read/write splitting.
// It uses the Primary for writes and load balances reads across the ReadReplicas.
func New(ctx context.Context, cfg Config) (*gorm.DB, error) {
	slog.Info("Connecting to primary database...")
	db, err := gorm.Open(postgres.Open(cfg.PrimaryDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), // Use a silent logger by default in prod
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to primary database: %w", err)
	}

	if len(cfg.ReadReplicas) > 0 {
		slog.Info("Configuring read replicas...", "count", len(cfg.ReadReplicas))
		resolverConfig := dbresolver.Config{
			Replicas: []gorm.Dialector{},
		}
		for _, replicaDSN := range cfg.ReadReplicas {
			resolverConfig.Replicas = append(resolverConfig.Replicas, postgres.Open(replicaDSN))
		}

		dbResolver := dbresolver.Register(resolverConfig,
			// You can add models here to default to read-only, e.g., &User{}
		)

		err = db.Use(dbResolver)
		if err != nil {
			return nil, fmt.Errorf("failed to configure db resolver: %w", err)
		}
		slog.Info("Read replicas configured successfully.")
	} else {
		slog.Warn("No read replicas configured. All queries will go to the primary.")
	}

	// Ping the database to ensure connection is alive
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	slog.Info("Database connection successful.")
	return db, nil
}
