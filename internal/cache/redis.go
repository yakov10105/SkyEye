package cache

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
)

// Client wraps the Redis client and the Redsync distributed lock factory.
type Client struct {
	rdb    *redis.Client
	rs     *redsync.Redsync
	logger *slog.Logger
}

// NewClient creates and returns a new Redis client. It pings the server to
// verify the connection. It also initializes a Redsync factory for distributed locks.
func NewClient(ctx context.Context, addr string) (*Client, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	pool := goredis.NewPool(rdb)
	rs := redsync.New(pool)

	logger.Info("Successfully connected to Redis", "address", addr)

	return &Client{
		rdb:    rdb,
		rs:     rs,
		logger: logger,
	}, nil
}

// Lock acquires a distributed lock for the given key. It returns a mutex object
// that must be used to unlock. Returns an error if the lock cannot be acquired.
func (c *Client) Lock(ctx context.Context, key string, ttl time.Duration) (*redsync.Mutex, error) {
	mutex := c.rs.NewMutex(key, redsync.WithExpiry(ttl))
	if err := mutex.LockContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to acquire lock for key %s: %w", key, err)
	}
	c.logger.Debug("Lock acquired", "key", key)
	return mutex, nil
}

// Unlock releases the given distributed lock.
func (c *Client) Unlock(ctx context.Context, mutex *redsync.Mutex) (bool, error) {
	ok, err := mutex.UnlockContext(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to unlock key %s: %w", mutex.Name(), err)
	}
	if ok {
		c.logger.Debug("Lock released", "key", mutex.Name())
	}
	return ok, nil
}

// Eval executes a Lua script on the Redis server.
func (c *Client) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	result, err := c.rdb.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to execute lua script: %w", err)
	}
	return result, nil
}

// Close gracefully closes the Redis connection.
func (c *Client) Close() error {
	c.logger.Info("Closing Redis connection")
	return c.rdb.Close()
}
