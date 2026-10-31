package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/queue"
)

// patient keeps a Wait from timing out in a test that expects an answer.
const patient = time.Minute

func TestWaitReturnsWhenJobCompletes(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	out := startWait(t, q, ctx, job.ID, patient)

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Complete(ctx, *d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := receive(t, out)
	if got.err != nil || got.res != (queue.WaitResult{Outcome: queue.WaitDone}) {
		t.Fatalf("Wait = %+v, %v, want WaitDone", got.res, got.err)
	}
	if n := q.WaiterCount(); n != 0 {
		t.Errorf("waiters = %d, want 0", n)
	}
}

func TestWaitReturnsFailure(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	out := startWait(t, q, ctx, job.ID, patient)

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Fail(ctx, *d, "invalid image"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := receive(t, out)
	want := queue.WaitResult{Outcome: queue.WaitFailed, Error: "invalid image"}
	if got.err != nil || got.res != want {
		t.Fatalf("Wait = %+v, %v, want %+v", got.res, got.err, want)
	}
}

func TestWaitTimesOut(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)

	res, err := q.Wait(context.Background(), job.ID, 100*time.Millisecond)

	if err != nil || res.Outcome != queue.WaitTimedOut {
		t.Fatalf("Wait = %+v, %v, want WaitTimedOut", res, err)
	}
	if n := q.WaiterCount(); n != 0 {
		t.Errorf("waiters = %d, want 0", n)
	}
}

func TestWaitIgnoresOtherJobs(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	r.publishForeignDone(t)

	res, err := q.Wait(context.Background(), job.ID, 300*time.Millisecond)

	if err != nil || res.Outcome != queue.WaitTimedOut {
		t.Fatalf("Wait = %+v, %v, want WaitTimedOut", res, err)
	}
}

func TestWaitSeesJobFinishedBeforeWaiting(t *testing.T) {
	q, r := newQueue(t)
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)

	res, err := q.Wait(context.Background(), testJob().ID, patient)

	if err != nil || res.Outcome != queue.WaitDone {
		t.Fatalf("Wait = %+v, %v, want WaitDone", res, err)
	}
}

func TestWaitSeesFailureBeforeWaiting(t *testing.T) {
	q, r := newQueue(t)
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	r.setFailed(t, testJob().ID, "image too large", time.Minute)

	res, err := q.Wait(context.Background(), testJob().ID, patient)

	want := queue.WaitResult{Outcome: queue.WaitFailed, Error: "image too large"}
	if err != nil || res != want {
		t.Fatalf("Wait = %+v, %v, want %+v", res, err, want)
	}
}

func TestWaitRecoversFromLostMessage(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	// No listener runs, as if the notice never arrived.
	out := startWait(t, q, context.Background(), job.ID, patient)

	if err := r.Del(context.Background(), "resize:job:"+job.ID).Err(); err != nil {
		t.Fatalf("removing state: %v", err)
	}

	got := receive(t, out)
	if got.err != nil || got.res.Outcome != queue.WaitDone {
		t.Fatalf("Wait = %+v, %v, want WaitDone", got.res, got.err)
	}
}

func TestWaitServesSeveralWaiters(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	ctx := context.Background()
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	first := startWait(t, q, ctx, job.ID, patient)
	second := startWait(t, q, ctx, job.ID, patient)

	d, _ := q.Read(ctx, "w1", time.Second)
	if err := q.Complete(ctx, *d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, out := range []<-chan waitReturn{first, second} {
		got := receive(t, out)
		if got.err != nil || got.res.Outcome != queue.WaitDone {
			t.Errorf("waiter %d: Wait = %+v, %v, want WaitDone", i, got.res, got.err)
		}
	}
	if n := q.WaiterCount(); n != 0 {
		t.Errorf("waiters = %d, want 0", n)
	}
}

func TestWaitStopsWhenClientDisconnects(t *testing.T) {
	q, r, job := readyQueue(t, time.Hour)
	q.SetRecheckInterval(time.Hour)
	listen(t, q, r)
	ctx, cancel := context.WithCancel(context.Background())
	out := startWait(t, q, ctx, job.ID, patient)

	cancel()

	got := receive(t, out)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", got.err)
	}
	if n := q.WaiterCount(); n != 0 {
		t.Errorf("waiters = %d, want 0", n)
	}
	if depth, err := q.Depth(context.Background()); err != nil || depth != 1 {
		t.Errorf("depth = %d, %v, want 1", depth, err)
	}
}
