package cache_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"SkyEye/internal/cache"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testClient *cache.Client
	redisURI   string
	logger     *slog.Logger
)

func TestMain(m *testing.M) {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pool, err := dockertest.NewPool("")
	if err != nil {
		logger.Error("Could not connect to docker", "error", err)
		os.Exit(1)
	}

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "redis",
		Tag:        "7-alpine",
	}, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		logger.Error("Could not start redis resource", "error", err)
		os.Exit(1)
	}

	redisURI = fmt.Sprintf("localhost:%s", resource.GetPort("6379/tcp"))

	// Exponential backoff-retry, because the application in the container might not be ready to accept connections yet
	if err := pool.Retry(func() error {
		var err error
		// Use a direct redis client to ping, not our wrapper
		rdb := redis.NewClient(&redis.Options{Addr: redisURI})
		return rdb.Ping(context.Background()).Err()
	}); err != nil {
		logger.Error("Could not connect to docker", "error", err)
		os.Exit(1)
	}

	testClient, err = cache.NewClient(context.Background(), redisURI)
	if err != nil {
		logger.Error("Could not create cache client", "error", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := pool.Purge(resource); err != nil {
		logger.Error("Could not purge resource", "error", err)
	}

	os.Exit(code)
}

func TestDistributedLocking(t *testing.T) {
	ctx := context.Background()
	lockKey := "my-distributed-lock"
	lockTTL := 2 * time.Second

	// 1. Acquire the lock
	mutex1, err := testClient.Lock(ctx, lockKey, lockTTL)
	require.NoError(t, err, "Should be able to acquire lock")
	assert.NotNil(t, mutex1)

	// 2. Try to acquire the same lock again (should fail)
	_, err = testClient.Lock(ctx, lockKey, lockTTL)
	assert.Error(t, err, "Should not be able to acquire the same lock")
	logger.Info("Successfully tested that a second lock acquisition fails as expected", "error", err)

	// 3. Release the lock
	ok, err := testClient.Unlock(ctx, mutex1)
	assert.NoError(t, err)
	assert.True(t, ok, "Should be able to release the lock")

	// 4. Acquire the lock again (should succeed)
	mutex2, err := testClient.Lock(ctx, lockKey, lockTTL)
	require.NoError(t, err, "Should be able to acquire lock after release")
	assert.NotNil(t, mutex2)

	// 5. Release the second lock
	ok, err = testClient.Unlock(ctx, mutex2)
	assert.NoError(t, err)
	assert.True(t, ok, "Should be able to release the second lock")
}

func TestLockExpiration(t *testing.T) {
	ctx := context.Background()
	lockKey := "my-expiring-lock"
	lockTTL := 1 * time.Second

	// 1. Acquire the lock
	mutex1, err := testClient.Lock(ctx, lockKey, lockTTL)
	require.NoError(t, err, "Should be able to acquire lock")
	assert.NotNil(t, mutex1)

	// 2. Wait for the lock to expire
	time.Sleep(lockTTL + 500*time.Millisecond)

	// 3. Try to acquire the lock again (should succeed)
	mutex2, err := testClient.Lock(ctx, lockKey, lockTTL)
	require.NoError(t, err, "Should be able to acquire lock after it expires")
	assert.NotNil(t, mutex2)

	// 4. Try to release the first, expired lock (should fail)
	// The redsync library may not return an error, but `ok` should be false.
	ok, err := testClient.Unlock(ctx, mutex1)
	assert.NoError(t, err, "Unlocking an expired mutex might not error, but should fail")
	assert.False(t, ok, "Should not be able to release an expired lock")

	// 5. Release the second lock
	ok, err = testClient.Unlock(ctx, mutex2)
	assert.NoError(t, err)
	assert.True(t, ok, "Should be able to release the second lock")
}

func TestEvalLuaScript(t *testing.T) {
	ctx := context.Background()
	
	t.Run("simple script", func(t *testing.T) {
		script := "return ARGV[1]"
		args := []interface{}{"hello world"}
		result, err := testClient.Eval(ctx, script, []string{}, args...)
		
		require.NoError(t, err)
		assert.Equal(t, "hello world", result)
	})

	t.Run("script with keys", func(t *testing.T) {
		key := "mykey"
		value := "myvalue"
		
		// Setup: set a key in Redis
		directClient := redis.NewClient(&redis.Options{Addr: redisURI})
		err := directClient.Set(ctx, key, value, 0).Err()
		require.NoError(t, err)

		script := "return redis.call('get', KEYS[1])"
		result, err := testClient.Eval(ctx, script, []string{key})

		require.NoError(t, err)
		assert.Equal(t, value, result)
	})
}
