package queue_test

import (
	"context"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type redisHandle struct {
	*redis.Client
}

// setFailed writes the state a worker leaves behind after a failed job.
func (r *redisHandle) setFailed(t *testing.T, id, msg string, ttl time.Duration) {
	t.Helper()

	ctx := context.Background()
	key := "resize:job:" + id
	if err := r.HSet(ctx, key, "status", "failed", "error", msg).Err(); err != nil {
		t.Fatalf("setting failed state: %v", err)
	}
	if err := r.PExpire(ctx, key, ttl).Err(); err != nil {
		t.Fatalf("expiring failed state: %v", err)
	}
}
