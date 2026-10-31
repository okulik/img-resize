package metrics

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const timeout = 30 * time.Second

func TestServeExposesMetrics(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Serve(ctx, 0) }()

	var addr string
	eventually(t, "the listener", func() bool { addr = m.Addr(); return addr != "" })

	res, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	for _, name := range []string{
		"resize_queue_depth", "resize_job_duration_seconds", `resize_jobs_total{result="abandoned"}`,
		`resize_throttled_total{limit="bytes"}`, `resize_rejected_total{reason="queue_full"}`,
		`resize_bandwidth_bytes_total{direction="served"}`,
	} {
		if !strings.Contains(string(body), name) {
			t.Errorf("scrape lacks %s", name)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("Serve did not stop with its context")
	}
}

func TestServeFailsWhenPortIsTaken(t *testing.T) {
	first := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = first.Serve(ctx, 0) }()
	var addr string
	eventually(t, "the listener", func() bool { addr = first.Addr(); return addr != "" })
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if err := New().Serve(context.Background(), uint(n)); err == nil {
		t.Error("expected an error for a port in use")
	}
}

func TestSampleQueueDepth(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.SampleQueueDepth(ctx, func(context.Context) (int64, error) { return 7, nil })

	eventually(t, "the gauge", func() bool { return testutil.ToFloat64(m.QueueDepth) == 7 })
}

func TestSampleQueueDepthRepeats(t *testing.T) {
	m := New()
	m.sampleEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var depth atomic.Int64
	depth.Store(1)
	go m.SampleQueueDepth(ctx, func(context.Context) (int64, error) { return depth.Load(), nil })

	eventually(t, "the first value", func() bool { return testutil.ToFloat64(m.QueueDepth) == 1 })
	depth.Store(5)
	eventually(t, "the next value", func() bool { return testutil.ToFloat64(m.QueueDepth) == 5 })
}

func TestSampleKeepsLastValueOnError(t *testing.T) {
	m := New()
	m.sampleEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	go m.SampleQueueDepth(ctx, func(context.Context) (int64, error) {
		if calls.Add(1) == 1 {
			return 4, nil
		}
		return 0, errors.New("injected error")
	})

	eventually(t, "several failed reads", func() bool { return calls.Load() >= 4 })
	if got := testutil.ToFloat64(m.QueueDepth); got != 4 {
		t.Errorf("unexpected depth: got %v want 4", got)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
