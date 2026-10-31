// Package testredis gives tests a clean database on a real Redis server.
package testredis

import (
	"context"
	"os"
	"testing"

	redis "github.com/redis/go-redis/v9"
)

// Client returns a client for database db of the Redis server named by
// SVC_TEST_REDIS_ADDR, flushed before use. The test is skipped if the variable
// is unset. Each package must use its own db so packages can test in parallel.
func Client(t *testing.T, db int) *redis.Client {
	t.Helper()

	addr := os.Getenv("SVC_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("SVC_TEST_REDIS_ADDR not set")
	}

	client := redis.NewClient(&redis.Options{Addr: addr, DB: db})
	t.Cleanup(func() { _ = client.Close() })

	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flushing test redis db %d: %v", db, err)
	}

	return client
}
