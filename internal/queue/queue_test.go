package queue_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
)

func newQueue(t *testing.T) (*queue.Queue, *redisHandle) {
	t.Helper()

	client := testredis.Client(t, 2)
	cfg := &settings.Settings{Service: &settings.ServiceSettings{
		JobStateTTL:  time.Hour,
		FailedJobTTL: 5 * time.Minute,
	}}

	return queue.New(client, cfg), &redisHandle{client}
}

func testJob() model.Job {
	return model.Job{
		ID:     model.ImageID("http://example.com/a.jpg", 100, 50),
		URL:    "http://example.com/a.jpg",
		Width:  100,
		Height: 50,
	}
}

func TestSubmitCreatesOneJob(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	job := testJob()

	got, err := q.Submit(ctx, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != queue.Created {
		t.Fatalf("submission = %v, want Created", got)
	}

	depth, err := q.Depth(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if depth != 1 {
		t.Fatalf("depth = %d, want 1", depth)
	}

	state, found, err := q.State(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || state.Status != queue.StatusQueued || state.Error != "" {
		t.Fatalf("state = %+v found = %v, want queued", state, found)
	}
}

func TestSubmitWritesStreamEntry(t *testing.T) {
	q, r := newQueue(t)
	job := testJob()

	if _, err := q.Submit(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, err := r.XRange(context.Background(), "resize:jobs", "-", "+").Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("stream entries = %d, err = %v, want 1", len(msgs), err)
	}
	want := map[string]any{"id": job.ID, "url": job.URL, "width": "100", "height": "50"}
	for k, v := range want {
		if msgs[0].Values[k] != v {
			t.Errorf("field %s = %v, want %v", k, msgs[0].Values[k], v)
		}
	}
}

func TestSubmitDuplicateIsActive(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	job := testJob()

	if _, err := q.Submit(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := q.Submit(ctx, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != queue.Active {
		t.Fatalf("submission = %v, want Active", got)
	}

	depth, _ := q.Depth(ctx)
	if depth != 1 {
		t.Fatalf("depth = %d, want 1", depth)
	}
}

func TestSubmitConcurrentCreatesOneJob(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	job := testJob()

	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := q.Submit(ctx, job)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got == queue.Created {
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	if created.Load() != 1 {
		t.Fatalf("created = %d, want 1", created.Load())
	}
	depth, _ := q.Depth(ctx)
	if depth != 1 {
		t.Fatalf("depth = %d, want 1", depth)
	}
}

func TestSubmitRepeatsAfterFailedStateExpires(t *testing.T) {
	q, r := newQueue(t)
	ctx := context.Background()
	job := testJob()

	r.setFailed(t, job.ID, "invalid image", 100*time.Millisecond)

	got, err := q.Submit(ctx, job)
	if err != nil || got != queue.Failed {
		t.Fatalf("submission = %v err = %v, want Failed", got, err)
	}

	time.Sleep(200 * time.Millisecond)

	got, err = q.Submit(ctx, job)
	if err != nil || got != queue.Created {
		t.Fatalf("submission = %v err = %v, want Created", got, err)
	}
	depth, _ := q.Depth(ctx)
	if depth != 1 {
		t.Fatalf("depth = %d, want 1", depth)
	}
}

func TestSubmitFailedStateReportsFailed(t *testing.T) {
	q, r := newQueue(t)
	ctx := context.Background()
	job := testJob()

	r.setFailed(t, job.ID, "fetch failed", time.Minute)

	got, err := q.Submit(ctx, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != queue.Failed {
		t.Fatalf("submission = %v, want Failed", got)
	}
	depth, _ := q.Depth(ctx)
	if depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}

	state, found, err := q.State(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || state.Status != queue.StatusFailed || state.Error != "fetch failed" {
		t.Fatalf("state = %+v found = %v, want failed with error", state, found)
	}
}

func TestStateMissing(t *testing.T) {
	q, _ := newQueue(t)

	_, found, err := q.State(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("found = true, want false")
	}
}
