package service_test

import (
	"bytes"
	"context"
	"fmt"
	stdimage "image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/ratelimit"
	"github.com/okulik/img-resize/internal/service"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
	"github.com/okulik/img-resize/internal/worker"
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

func TestServiceFinishesInFlightRequestsOnShutdown(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)

	started := make(chan struct{})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		_ = jpeg.Encode(w, stdimage.NewRGBA(stdimage.Rect(0, 0, 8, 8)), nil)
	}))
	t.Cleanup(source.Close)

	settings := buildServiceSettings(t)
	settings.Http.ServerGracefulShutdownTimeout = 10 * time.Second
	done := startService(t, settings)
	waitForHealthy(t, settings)

	body := fmt.Sprintf(`{"urls":[%q],"width":4}`, source.URL)
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/resize?sync=true", settings.Http.ServerPort), strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	req.SetBasicAuth("admin", "admin")

	type reply struct {
		status int
		data   []byte
		err    error
	}
	response := make(chan reply, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			response <- reply{err: err}
			return
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		response <- reply{status: res.StatusCode, data: data, err: err}
	}()

	<-started
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	got := <-response
	if got.err != nil || got.status != http.StatusOK {
		t.Fatalf("in-flight request did not finish: status %d, error %v, body %s", got.status, got.err, got.data)
	}
	if _, err := jpeg.Decode(bytes.NewReader(got.data)); err != nil {
		t.Errorf("in-flight request did not return an image: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("unexpected error %v", err)
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
		Service: &settings.ServiceSettings{
			MaxImageSize:            1024 * 1024,
			MaxImagePixels:          1024 * 1024,
			MaxImageDimension:       1024,
			MaxImageSourceDimension: 1024,
			AllowInternalURLs:       true,
			SyncWaitTimeout:         10 * time.Second,
			JobStateTTL:             time.Hour,
			FailedJobTTL:            time.Hour,
			JobClaimIdle:            time.Hour,
			JobReclaimInterval:      time.Hour,
			JobMaxAttempts:          3,
			WorkerConcurrency:       1,
			RateLimitPerMinute:      1000,
			RateLimitBytesPerMinute: 1 << 30,
			RateLimitIPv4Prefix:     32,
			RateLimitIPv6Prefix:     64,
			MaxQueueDepth:           100,
		},
		Auth: &settings.AuthSettings{Username: "admin", Password: "admin"},
		Http: &settings.HttpSettings{
			ServerPort:                    uint(listener.Addr().(*net.TCPAddr).Port),
			ServerGracefulShutdownTimeout: time.Second,
			ClientReadTimeout:             time.Second,
		},
	}
}

func startService(t *testing.T, settings *settings.Settings) <-chan error {
	t.Helper()
	client := testredis.Client(t, 6)
	imageCache, err := cache.NewLRUImageCache(1)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	jobs := queue.New(client, settings)

	// The worker stops with the test. A blocked read ends when the client closes.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	t.Cleanup(stopWorker)
	go func() {
		_ = worker.New(settings, jobs, imageCache, image.NewResizer(settings)).Run(workerCtx)
	}()

	done := make(chan error, 1)
	go func() {
		done <- service.NewService(settings, imageCache, jobs, ratelimit.New(client, time.Now)).Start()
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
