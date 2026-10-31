package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/okulik/img-resize/internal/metrics"
	"github.com/okulik/img-resize/internal/settings"
)

// Service runs a Worker with a health endpoint, for a process with the worker
// role.
type Service struct {
	settings *settings.Settings
	worker   *Worker
	metrics  *metrics.Metrics
}

func NewService(settings *settings.Settings, worker *Worker, metrics *metrics.Metrics) *Service {
	return &Service{settings: settings, worker: worker, metrics: metrics}
}

// Start runs the worker until the process is told to stop. It then stops
// reading, finishes the job it is running, and returns. /health stays up
// meanwhile, and is the only route.
func (svc *Service) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	server := http.Server{
		Addr:         fmt.Sprintf(":%d", svc.settings.Http.ServerPort),
		Handler:      mux,
		IdleTimeout:  svc.settings.Http.ServerIdleTimeout,
		ReadTimeout:  svc.settings.Http.ServerReadTimeout,
		WriteTimeout: svc.settings.Http.ServerWriteTimeout,
	}

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	log.Printf("started worker on port %d\n", svc.settings.Http.ServerPort)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metricsDone := make(chan struct{})
	go func() {
		defer close(metricsDone)
		svc.metrics.Run(ctx, svc.settings.Service.MetricsPort)
	}()
	workerDone := make(chan error, 1)
	go func() { workerDone <- svc.worker.Run(ctx) }()

	var failure error
	workerStopped := false
	serverStopped := false
	select {
	case sig := <-sigs:
		log.Printf("received signal %s, shutting down\n", sig.String())
	case err := <-serverErr:
		serverStopped = true
		failure = fmt.Errorf("error serving health: %w", err)
	case err := <-workerDone:
		workerStopped = true
		failure = err
	}

	cancel()
	if !workerStopped {
		failure = errors.Join(failure, <-workerDone)
	}
	<-metricsDone

	if !serverStopped {
		shutdownCtx, shutdownCancel :=
			context.WithTimeout(context.Background(), svc.settings.Http.ServerGracefulShutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			failure = errors.Join(failure, fmt.Errorf("error shutting down server: %w", err))
		}
	}

	return failure
}
