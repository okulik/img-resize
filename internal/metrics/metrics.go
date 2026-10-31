// Package metrics exposes the service's Prometheus metrics on a port of their
// own, which is not public.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	sampleInterval  = 5 * time.Second
	shutdownTimeout = 5 * time.Second

	// Values of the jobs counter's result label.
	ResultSuccess   = "success"
	ResultFailure   = "failure"
	ResultRetried   = "retried"
	ResultAbandoned = "abandoned"

	// Values of the throttled counter's limit label.
	LimitRequests = "requests"
	LimitBytes    = "bytes"

	// Values of the rejected counter's reason label.
	ReasonQueueFull = "queue_full"

	// Values of the bandwidth counter's direction label.
	DirectionStored = "stored"
	DirectionServed = "served"
)

// Metrics holds the service's metrics, each Metrics in a registry of its own.
type Metrics struct {
	QueueDepth  prometheus.Gauge
	JobDuration prometheus.Histogram
	Jobs        *prometheus.CounterVec
	Throttled   *prometheus.CounterVec
	Rejected    *prometheus.CounterVec
	Bandwidth   *prometheus.CounterVec

	registry    *prometheus.Registry
	sampleEvery time.Duration
	addr        atomic.Pointer[string]
}

func New() *Metrics {
	m := &Metrics{
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "resize_queue_depth",
			Help: "Jobs in the queue, as last sampled.",
		}),
		JobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "resize_job_duration_seconds",
			Help:    "Time a worker spent resizing one image.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30},
		}),
		Jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "resize_jobs_total",
			Help: "Jobs by result.",
		}, []string{"result"}),
		Throttled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "resize_throttled_total",
			Help: "Requests refused by a rate limit.",
		}, []string{"limit"}),
		Rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "resize_rejected_total",
			Help: "Requests refused for another reason.",
		}, []string{"reason"}),
		Bandwidth: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "resize_bandwidth_bytes_total",
			Help: "Image bytes stored by workers and served to clients.",
		}, []string{"direction"}),

		registry:    prometheus.NewRegistry(),
		sampleEvery: sampleInterval,
	}
	m.registry.MustRegister(m.QueueDepth, m.JobDuration, m.Jobs, m.Throttled, m.Rejected, m.Bandwidth)

	// Series exist from the start, so a rate over them has a baseline.
	for _, result := range []string{ResultSuccess, ResultFailure, ResultRetried, ResultAbandoned} {
		m.Jobs.WithLabelValues(result)
	}
	for _, limit := range []string{LimitRequests, LimitBytes} {
		m.Throttled.WithLabelValues(limit)
	}
	m.Rejected.WithLabelValues(ReasonQueueFull)
	for _, direction := range []string{DirectionStored, DirectionServed} {
		m.Bandwidth.WithLabelValues(direction)
	}

	return m
}

// Serve answers /metrics on all interfaces at port until ctx ends. Port 0 picks
// a free one. It returns an error only if it can't listen or the server breaks,
// and callers should log that rather than stop, as metrics are not worth an
// outage.
func (m *Metrics) Serve(ctx context.Context, port uint) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("listening for metrics: %w", err)
	}
	addr := listener.Addr().String()
	m.addr.Store(&addr)
	log.Printf("serving metrics on %s\n", addr)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))
	server := http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serving metrics: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down metrics: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving metrics: %w", err)
	}

	return nil
}

// Run is Serve for a caller that keeps going when metrics fail: it logs the
// error instead of returning it.
func (m *Metrics) Run(ctx context.Context, port uint) {
	if err := m.Serve(ctx, port); err != nil {
		log.Printf("metrics stopped: %v", err)
	}
}

// Addr is the address Serve listens on, or empty before it does.
func (m *Metrics) Addr() string {
	if addr := m.addr.Load(); addr != nil {
		return *addr
	}

	return ""
}

// SampleQueueDepth sets the queue depth gauge now and then every 5 seconds,
// until ctx ends. A failed read is logged and leaves the last value.
func (m *Metrics) SampleQueueDepth(ctx context.Context, depth func(context.Context) (int64, error)) {
	ticker := time.NewTicker(m.sampleEvery)
	defer ticker.Stop()

	for {
		if n, err := depth(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("sampling queue depth: %v", err)
		} else {
			m.QueueDepth.Set(float64(n))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
