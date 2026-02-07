package db_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"SkyEye/internal/db"

	"github.com/ory/dockertest/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

var (
	testDB *gorm.DB
	logger *slog.Logger
)

const (
	postgresUser     = "testuser"
	postgresPassword = "testpassword"
	postgresDBName   = "testdb"
)

type Product struct {
	gorm.Model
	Code  string
	Price uint
}

func TestMain(m *testing.M) {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pool, err := dockertest.NewPool("")
	if err != nil {
		logger.Error("Could not connect to docker", "error", err)
		os.Exit(1)
	}

	primaryRes, primaryDSN := startPostgresContainer(pool, "primary")
	replicaRes, replicaDSN := startPostgresContainer(pool, "replica")

	dbConfig := db.Config{
		PrimaryDSN:   primaryDSN,
		ReadReplicas: []string{replicaDSN},
	}
	testDB, err = db.New(context.Background(), dbConfig)
	if err != nil {
		logger.Error("Failed to connect to DB with resolver", "error", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := pool.Purge(primaryRes); err != nil {
		logger.Error("Could not purge primary resource", "error", err)
	}
	if err := pool.Purge(replicaRes); err != nil {
		logger.Error("Could not purge replica resource", "error", err)
	}

	os.Exit(code)
}

func startPostgresContainer(pool *dockertest.Pool, name string) (*dockertest.Resource, string) {
	runOpts := &dockertest.RunOptions{
		Name:       fmt.Sprintf("postgres-%s", name),
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_USER=" + postgresUser,
			"POSTGRES_PASSWORD=" + postgresPassword,
			"POSTGRES_DB=" + postgresDBName,
		},
	}

	res, err := pool.RunWithOptions(runOpts)
	if err != nil {
		logger.Error("Could not start container", "name", runOpts.Name, "error", err)
		os.Exit(1)
	}

	dsn := fmt.Sprintf("host=localhost port=%s user=%s password=%s dbname=%s sslmode=disable",
		res.GetPort("5432/tcp"), postgresUser, postgresPassword, postgresDBName)

	if err := pool.Retry(func() error {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			return err
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Ping()
	}); err != nil {
		logger.Error("Could not connect to container", "name", runOpts.Name, "error", err)
		os.Exit(1)
	}
	
	return res, dsn
}

func TestReadWriteSplitting(t *testing.T) {
	err := testDB.AutoMigrate(&Product{})
	require.NoError(t, err, "Failed to migrate schema on primary")
	
	// Defer cleanup of the table on the primary
	defer func() {
		testDB.Exec("DROP TABLE products;")
	}()

	t.Run("CreateOnPrimary_ReadOnReplicaFails_ReadOnPrimarySucceeds", func(t *testing.T) {
		productCode := "L121"
		
		// Create a record. This should go to the primary.
		createResult := testDB.Create(&Product{Code: productCode, Price: 1000})
		require.NoError(t, createResult.Error)
		
		// Try to find the record with a default read. It should fail because the
		// table/data does not exist on the read replica container.
		var replicaProduct Product
		err := testDB.Where("code = ?", productCode).First(&replicaProduct).Error
		assert.Error(t, err, "Expected an error when reading from replica, as data should not be there")
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "Expected 'record not found' from replica")

		// Explicitly find the record on the primary. This should succeed.
		var primaryProduct Product
		err = testDB.Clauses(dbresolver.Write).Where("code = ?", productCode).First(&primaryProduct).Error
		require.NoError(t, err, "Should find record on primary")
		assert.Equal(t, productCode, primaryProduct.Code)
	})
}
