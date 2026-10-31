package queue_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
)

func newQueue(t *testing.T) (*queue.Queue, *redisHandle) {
	t.Helper()

	return newQueueWithClaimIdle(t, time.Hour)
}

func newQueueWithClaimIdle(t *testing.T, claimIdle time.Duration) (*queue.Queue, *redisHandle) {
	t.Helper()

	return newQueueWith(t, func(s *settings.ServiceSettings) { s.JobClaimIdle = claimIdle })
}

func newQueueWith(t *testing.T, tweak func(*settings.ServiceSettings)) (*queue.Queue, *redisHandle) {
	t.Helper()

	client := testredis.Client(t, 2)
	svc := &settings.ServiceSettings{
		JobStateTTL:    time.Hour,
		FailedJobTTL:   5 * time.Minute,
		JobClaimIdle:   time.Hour,
		JobMaxAttempts: 3,
	}
	tweak(svc)
	cfg := &settings.Settings{Service: svc}

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

// readyQueue returns a queue with its group created and testJob submitted.
func readyQueue(t *testing.T, claimIdle time.Duration) (*queue.Queue, *redisHandle, model.Job) {
	t.Helper()

	q, r := newQueueWithClaimIdle(t, claimIdle)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := testJob()
	if _, err := q.Submit(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	return q, r, job
}

func pendingCount(t *testing.T, r *redisHandle) int64 {
	t.Helper()

	p, err := r.XPending(context.Background(), "resize:jobs", "resize-workers").Result()
	if err != nil {
		t.Fatalf("reading pending: %v", err)
	}

	return p.Count
}

func TestReadReturnsSubmittedJob(t *testing.T) {
	q, _, job := readyQueue(t, time.Hour)

	d, err := q.Read(context.Background(), "w1", time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil {
		t.Fatal("delivery = nil, want job")
	}
	if d.Job != job || d.Attempts != 1 || d.MessageID == "" {
		t.Fatalf("delivery = %+v, want job %+v with attempts 1", d, job)
	}
}

func TestReadReturnsNilWhenEmpty(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d, err := q.Read(ctx, "w1", 50*time.Millisecond)
	if err != nil || d != nil {
		t.Fatalf("delivery = %+v err = %v, want nil, nil", d, err)
	}
}

func TestReadBlocksUntilJobArrives(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = q.Submit(ctx, testJob())
	}()

	d, err := q.Read(ctx, "w1", 30*time.Second)
	if err != nil || d == nil {
		t.Fatalf("delivery = %+v err = %v, want job", d, err)
	}
}

func TestReadDeliversOnlyOnce(t *testing.T) {
	q, _, _ := readyQueue(t, time.Hour)
	ctx := context.Background()

	first, err := q.Read(ctx, "w1", time.Second)
	if err != nil || first == nil {
		t.Fatalf("first delivery = %+v err = %v, want job", first, err)
	}
	second, err := q.Read(ctx, "w2", 50*time.Millisecond)
	if err != nil || second != nil {
		t.Fatalf("second delivery = %+v err = %v, want nil, nil", second, err)
	}
}

func TestCompleteRemovesJob(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.MarkProcessing(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := q.Complete(ctx, *d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if _, found, _ := q.State(ctx, job.ID); found {
		t.Fatal("state found, want removed")
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestCompleteWorksWithoutState(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := r.Del(ctx, "resize:job:"+job.ID).Err(); err != nil {
		t.Fatalf("deleting state: %v", err)
	}
	if err := q.Complete(ctx, *d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestCompletePublishesID(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	sub := r.subscribeDone(t)
	r.publishForeignDone(t)
	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Complete(ctx, *d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r.waitDone(t, sub, job.ID)
}

func TestFailPublishesID(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	sub := r.subscribeDone(t)
	r.publishForeignDone(t)
	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Fail(ctx, *d, "fetch failed"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r.waitDone(t, sub, job.ID)
}

func TestFailStoresError(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Fail(ctx, *d, "fetch failed"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, found, _ := q.State(ctx, job.ID)
	if !found || state.Status != queue.StatusFailed || state.Error != "fetch failed" {
		t.Fatalf("state = %+v found = %v, want failed with fetch failed", state, found)
	}
	assertTTL(t, r, job.ID, 5*time.Minute)
	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestFailAppliesTTLWhenStateExpired(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := r.Del(ctx, "resize:job:"+job.ID).Err(); err != nil {
		t.Fatalf("deleting state: %v", err)
	}
	if err := q.Fail(ctx, *d, "invalid image"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, found, _ := q.State(ctx, job.ID)
	if !found || state.Status != queue.StatusFailed || state.Error != "invalid image" {
		t.Fatalf("state = %+v found = %v, want failed", state, found)
	}
	assertTTL(t, r, job.ID, 5*time.Minute)
}

// assertTTL fails unless the job's state key expires within max.
func assertTTL(t *testing.T, r *redisHandle, id string, max time.Duration) {
	t.Helper()

	ttl, err := r.PTTL(context.Background(), "resize:job:"+id).Result()
	if err != nil {
		t.Fatalf("reading ttl: %v", err)
	}
	if ttl <= 0 || ttl > max {
		t.Fatalf("ttl = %v, want in (0, %v]", ttl, max)
	}
}

func TestMarkProcessingSetsStatusAndTTL(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	if err := q.MarkProcessing(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, found, _ := q.State(ctx, job.ID)
	if !found || state.Status != queue.StatusProcessing {
		t.Fatalf("state = %+v found = %v, want processing", state, found)
	}
	assertTTL(t, r, job.ID, time.Hour)
}

func TestMarkProcessingAppliesTTLWhenStateExpired(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()

	if err := r.Del(ctx, "resize:job:"+job.ID).Err(); err != nil {
		t.Fatalf("deleting state: %v", err)
	}
	if err := q.MarkProcessing(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, found, _ := q.State(ctx, job.ID)
	if !found || state.Status != queue.StatusProcessing {
		t.Fatalf("state = %+v found = %v, want processing", state, found)
	}
	assertTTL(t, r, job.ID, time.Hour)
}

func TestReclaimReturnsIdleJob(t *testing.T) {
	q, _, job := readyQueue(t, 10*time.Millisecond)
	ctx := context.Background()

	if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
		t.Fatal("delivery = nil, want job")
	}
	time.Sleep(200 * time.Millisecond)

	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if abandoned != 0 || len(got) != 1 {
		t.Fatalf("deliveries = %d abandoned = %d, want 1 and 0", len(got), abandoned)
	}
	if got[0].Job != job || got[0].Attempts != 2 {
		t.Fatalf("delivery = %+v, want job %+v with attempts 2", got[0], job)
	}
}

func TestReclaimIgnoresBusyJob(t *testing.T) {
	q, _, _ := readyQueue(t, time.Hour)
	ctx := context.Background()

	if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
		t.Fatal("delivery = nil, want job")
	}

	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil || len(got) != 0 || abandoned != 0 {
		t.Fatalf("deliveries = %d abandoned = %d err = %v, want none", len(got), abandoned, err)
	}
}

func TestReclaimAbandonsAfterMaxAttempts(t *testing.T) {
	q, r, job := readyQueue(t, 10*time.Millisecond)
	ctx := context.Background()

	if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
		t.Fatal("delivery = nil, want job")
	}
	// Attempts 2 and 3 crash too.
	for want := int64(2); want <= 3; want++ {
		time.Sleep(200 * time.Millisecond)
		got, abandoned, err := q.Reclaim(ctx, "w2")
		if err != nil || abandoned != 0 || len(got) != 1 || got[0].Attempts != want {
			t.Fatalf("deliveries = %+v abandoned = %d err = %v, want attempts %d", got, abandoned, err, want)
		}
	}

	time.Sleep(200 * time.Millisecond)
	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 || abandoned != 1 {
		t.Fatalf("deliveries = %d abandoned = %d, want 0 and 1", len(got), abandoned)
	}

	state, found, _ := q.State(ctx, job.ID)
	if !found || state.Status != queue.StatusFailed || state.Error != "fetch failed" {
		t.Fatalf("state = %+v found = %v, want failed with fetch failed", state, found)
	}
	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestReadDropsMalformedEntry(t *testing.T) {
	q, r := newQueue(t)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The width is not a number.
	err := r.XAdd(ctx, &redis.XAddArgs{Stream: "resize:jobs", Values: map[string]any{
		"id": "x", "url": "http://example.com/a.jpg", "width": "wide", "height": "50",
	}}).Err()
	if err != nil {
		t.Fatalf("adding entry: %v", err)
	}

	d, err := q.Read(ctx, "w1", time.Second)
	if err != nil || d != nil {
		t.Fatalf("delivery = %+v err = %v, want nil, nil", d, err)
	}
	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestReclaimDropsMalformedEntry(t *testing.T) {
	q, r := newQueueWithClaimIdle(t, 10*time.Millisecond)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := r.XAdd(ctx, &redis.XAddArgs{Stream: "resize:jobs", Values: map[string]any{"id": "x"}}).Err()
	if err != nil {
		t.Fatalf("adding entry: %v", err)
	}
	// Deliver it without going through Read's validation.
	if err := r.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: "resize-workers", Consumer: "w1", Streams: []string{"resize:jobs", ">"}, Count: 1,
	}).Err(); err != nil {
		t.Fatalf("reading raw: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil || len(got) != 0 || abandoned != 0 {
		t.Fatalf("deliveries = %d abandoned = %d err = %v, want none", len(got), abandoned, err)
	}
	if depth, _ := q.Depth(ctx); depth != 0 {
		t.Fatalf("depth = %d, want 0", depth)
	}
	if n := pendingCount(t, r); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}
}

func TestEnsureGroupIsRepeatable(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := q.EnsureGroup(ctx); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}
}

func TestEnsureGroupSeesExistingJobs(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	job := testJob()

	if _, err := q.Submit(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	d, err := q.Read(ctx, "w1", time.Second)
	if err != nil || d == nil || d.Job != job {
		t.Fatalf("delivery = %+v err = %v, want job %+v", d, err, job)
	}
}

func otherJob() model.Job {
	return model.Job{
		ID:     model.ImageID("http://example.com/b.jpg", 100, 50),
		URL:    "http://example.com/b.jpg",
		Width:  100,
		Height: 50,
	}
}

func TestReclaimClaimsOneJob(t *testing.T) {
	q, r := newQueueWith(t, func(s *settings.ServiceSettings) {
		s.JobClaimIdle = 10 * time.Millisecond
		s.WorkerConcurrency = 2
	})
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, job := range []model.Job{testJob(), otherJob()} {
		if _, err := q.Submit(ctx, job); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	for i := 0; i < 2; i++ {
		if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
			t.Fatal("delivery = nil, want job")
		}
	}
	time.Sleep(200 * time.Millisecond)

	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil || len(got) != 1 || abandoned != 0 {
		t.Fatalf("deliveries = %d abandoned = %d err = %v, want 1 and 0", len(got), abandoned, err)
	}

	pending, err := r.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: "resize:jobs", Group: "resize-workers", Start: "-", End: "+", Count: 10,
	}).Result()
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %d err = %v, want 2", len(pending), err)
	}
	for _, p := range pending {
		want := int64(1)
		if p.ID == got[0].MessageID {
			want = 2
		}
		if p.RetryCount != want {
			t.Fatalf("message %s delivery count = %d, want %d", p.ID, p.RetryCount, want)
		}
	}
}

func TestReclaimSkipsAbandonedToFindRunnableJob(t *testing.T) {
	q, _ := newQueueWith(t, func(s *settings.ServiceSettings) {
		s.JobClaimIdle = 10 * time.Millisecond
		s.JobMaxAttempts = 2
	})
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The first job runs twice and crashes both times.
	if _, err := q.Submit(ctx, testJob()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
		t.Fatal("delivery = nil, want job")
	}
	time.Sleep(200 * time.Millisecond)
	if got, _, err := q.Reclaim(ctx, "w1"); err != nil || len(got) != 1 {
		t.Fatalf("deliveries = %d err = %v, want 1", len(got), err)
	}

	// The second job sits behind it in the pending list.
	if _, err := q.Submit(ctx, otherJob()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d, _ := q.Read(ctx, "w1", time.Second); d == nil {
		t.Fatal("delivery = nil, want job")
	}
	time.Sleep(200 * time.Millisecond)

	got, abandoned, err := q.Reclaim(ctx, "w2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if abandoned != 1 || len(got) != 1 || got[0].Job != otherJob() {
		t.Fatalf("deliveries = %+v abandoned = %d, want the second job and 1 abandoned", got, abandoned)
	}
}

func TestReclaimSkipsMalformedToFindRunnableJob(t *testing.T) {
	q, r := newQueueWithClaimIdle(t, 10*time.Millisecond)
	ctx := context.Background()
	if err := q.EnsureGroup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := r.XAdd(ctx, &redis.XAddArgs{Stream: "resize:jobs", Values: map[string]any{"id": "x"}}).Err()
	if err != nil {
		t.Fatalf("adding entry: %v", err)
	}
	if _, err := q.Submit(ctx, testJob()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := r.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: "resize-workers", Consumer: "w1", Streams: []string{"resize:jobs", ">"}, Count: 2,
	}).Err(); err != nil {
		t.Fatalf("reading raw: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	got, _, err := q.Reclaim(ctx, "w2")
	if err != nil || len(got) != 1 || got[0].Job != testJob() {
		t.Fatalf("deliveries = %+v err = %v, want the valid job", got, err)
	}
}
