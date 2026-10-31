package service_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/service"
	"github.com/okulik/img-resize/internal/settings"
)

func TestServiceShutsDownOnSIGTERM(t *testing.T) {
	// Keeps an unhandled SIGTERM from killing the test binary.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)

	settings := buildServiceSettings(t)
	done := startService(t, settings)
	waitForHealthy(t, settings)

	deadline := time.After(2 * time.Second)
	for {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatalf("unexpected error %v", err)
		}

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			return
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("service did not shut down after SIGTERM")
		}
	}
}

func buildServiceSettings(t *testing.T) *settings.Settings {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	defer listener.Close()

	return &settings.Settings{
		Service: &settings.ServiceSettings{},
		Auth:    &settings.AuthSettings{Username: "admin", Password: "admin"},
		Http: &settings.HttpSettings{
			ServerPort:                    uint(listener.Addr().(*net.TCPAddr).Port),
			ServerGracefulShutdownTimeout: time.Second,
		},
	}
}

func startService(t *testing.T, settings *settings.Settings) <-chan error {
	t.Helper()
	imageCache, err := cache.NewLRUImageCache(1)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- service.NewService(settings, imageCache, image.NewResizer(settings, imageCache)).Start()
	}()

	return done
}

func waitForHealthy(t *testing.T, settings *settings.Settings) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", settings.Http.ServerPort)
	for range 100 {
		if res, err := http.Get(url); err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service did not become healthy")
}
