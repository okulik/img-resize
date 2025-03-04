package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"

	"github.com/okulik/fm-go/internal/image"
	"github.com/okulik/fm-go/internal/settings"
)

type Service struct {
	settings   *settings.Settings
	imageCache image.ImageCacheAdapter
	resizer    *image.Resizer
}

func NewService(settings *settings.Settings, imageCache image.ImageCacheAdapter, resizer *image.Resizer) *Service {
	return &Service{
		settings:   settings,
		imageCache: imageCache,
		resizer:    resizer,
	}
}

func (svc *Service) Start() error {
	server, cancel := svc.startHttpServer()
	defer cancel()

	log.Printf("started http server on port %d\n", svc.settings.Http.ServerPort)

	if err := svc.handleSignals(server); err != nil {
		return errors.Wrap(err, "error shutting down server")
	}

	return nil
}

func (svc *Service) startHttpServer() (*http.Server, context.CancelFunc) {
	// Create a context that will be used as a http handler methods'
	// base context and that will be called on server's graceful
	// shutdown.
	baseCtx, baseCancel := context.WithCancel(context.Background())
	server := http.Server{
		Addr:         fmt.Sprintf(":%d", svc.settings.Http.ServerPort),
		Handler:      NewRouter(svc.settings, svc.imageCache, svc.resizer),
		BaseContext:  func(_ net.Listener) context.Context { return baseCtx },
		IdleTimeout:  svc.settings.Http.ServerIdleTimeout,
		ReadTimeout:  svc.settings.Http.ServerReadTimeout,
		WriteTimeout: svc.settings.Http.ServerWriteTimeout,
	}
	// Register the base context cancel function with the server's
	// OnShutdown hook so that it will be called on server's graceful
	// shutdown. This will allow all http handler methods to finish
	// their work before the server is shutdown.
	server.RegisterOnShutdown(baseCancel)

	// Start the http server in a goroutine so that it doesn't block.
	// If the server fails to start, log the error and exit.
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("error starting http server: %v", err)
		}
	}()

	return &server, baseCancel
}

func (svc *Service) handleSignals(server *http.Server) error {
	// Create a channel to listen for an interrupt or terminate signals
	// from the OS.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	sig := <-sigs
	log.Printf("received signal %s, shutting down\n", sig.String())

	// Create a deadline for a graceful shutdown of the http server.
	gracefulCtx, gracefulCancel :=
		context.WithTimeout(context.Background(), svc.settings.Http.ServerGracefulShutdownTimeout)
	defer gracefulCancel()

	if err := server.Shutdown(gracefulCtx); err != nil {
		return err
	}

	svc.resizer.Shutdown()

	return nil
}
