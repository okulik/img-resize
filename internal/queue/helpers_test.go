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

// subscribeDone subscribes to the completion channel and waits for the
// subscription to be confirmed, so no message published afterwards is missed.
func (r *redisHandle) subscribeDone(t *testing.T) *redis.PubSub {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sub := r.Subscribe(ctx, "resize:done")
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribing: %v", err)
	}

	return sub
}

// publishForeignDone announces a job of another package's test. Redis pub/sub
// ignores database numbers and packages test in parallel, so that really happens.
func (r *redisHandle) publishForeignDone(t *testing.T) {
	t.Helper()

	if err := r.Publish(context.Background(), "resize:done", "foreign-job").Err(); err != nil {
		t.Fatalf("publishing: %v", err)
	}
}

// waitDone reads the completion channel until it announces id, skipping what
// other packages' tests publish, and fails the test if id never comes.
func (r *redisHandle) waitDone(t *testing.T, sub *redis.PubSub, id string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		msg, err := sub.ReceiveMessage(ctx)
		if err != nil {
			t.Fatalf("waiting for %s on the done channel: %v", id, err)
		}
		if msg.Payload == id {
			return
		}
	}
}
