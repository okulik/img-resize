package queue_test

import (
	"context"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/queue"
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

// listen runs q.Listen until the test ends and returns once it delivers
// notices. It publishes a probe ID until a waiter registered for it is woken,
// as counting subscribers would count other packages' too.
func listen(t *testing.T, q *queue.Queue, r *redisHandle) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Listen(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	const probeID = "listen-probe"
	woken, remove := q.ProbeWaiter(probeID)
	defer remove()

	deadline := time.After(10 * time.Second)
	for {
		if err := r.Publish(context.Background(), "resize:done", probeID).Err(); err != nil {
			t.Fatalf("publishing probe: %v", err)
		}
		select {
		case <-woken:
			return
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("timed out waiting for the listener to subscribe")
		}
	}
}

// eventually polls cond until it holds, failing the test after 10 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// startWait runs q.Wait in the background and returns once it is registered.
func startWait(t *testing.T, q *queue.Queue, ctx context.Context, id string, timeout time.Duration) <-chan waitReturn {
	t.Helper()

	before := q.WaiterCount()
	out := make(chan waitReturn, 1)
	go func() {
		res, err := q.Wait(ctx, id, timeout)
		out <- waitReturn{res, err}
	}()
	eventually(t, "the waiter to register", func() bool { return q.WaiterCount() > before })

	return out
}

type waitReturn struct {
	res queue.WaitResult
	err error
}

// receive returns what a Wait returned, failing the test if it takes over 10 seconds.
func receive(t *testing.T, out <-chan waitReturn) waitReturn {
	t.Helper()

	select {
	case got := <-out:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return")
		return waitReturn{}
	}
}
