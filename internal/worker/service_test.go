package worker

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/settings"
)

func TestWorkerServiceHealth(t *testing.T) {
	// Keeps an unhandled SIGTERM from killing the test binary.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)

	f := newFixture(t, noRetry)
	port := freePort(t)
	f.settings.Http = httpSettings(port)
	done := startWorkerService(t, f)

	res := getHealth(t, port)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if res.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Errorf("unexpected health response: %d %q", res.StatusCode, body)
	}

	other, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/resize", port))
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("unexpected status for another route: %d", other.StatusCode)
	}

	stopWorkerService(t, done)
}

func TestWorkerServiceServesMetrics(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)

	f := newFixture(t, noRetry)
	f.settings.Http = httpSettings(freePort(t))
	done := startWorkerService(t, f)

	var addr string
	eventually(t, "the metrics listener", func() bool { addr = f.metrics.Addr(); return addr != "" })
	res, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("unexpected status: %d", res.StatusCode)
	}

	stopWorkerService(t, done)
}

func TestWorkerServiceFinishesJobOnSIGTERM(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	defer signal.Stop(sigs)

	f := newFixture(t, noRetry)
	port := freePort(t)
	f.settings.Http = httpSettings(port)
	src, g := gatedSource(t)
	job := f.submit(t, src.URL)
	done := startWorkerService(t, f)

	g.waitStarted(t)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	select {
	case err := <-done:
		t.Fatalf("Start returned before the job finished: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(g.opened)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("Start did not return after SIGTERM")
	}

	f.requireResized(t, job)
	f.requireFinished(t, job)
}

func httpSettings(port uint) *settings.HttpSettings {
	return &settings.HttpSettings{
		ServerPort:                    port,
		ServerGracefulShutdownTimeout: time.Second,
		ClientReadTimeout:             timeout,
	}
}

func freePort(t *testing.T) uint {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	defer listener.Close()

	return uint(listener.Addr().(*net.TCPAddr).Port)
}

func startWorkerService(t *testing.T, f *fixture) <-chan error {
	t.Helper()

	w := New(f.settings, f.queue, f.cache, f.resizer, f.metrics)
	w.readBlock = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- NewService(f.settings, w, f.metrics).Start() }()

	return done
}

func stopWorkerService(t *testing.T, done <-chan error) {
	t.Helper()

	deadline := time.After(timeout)
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

func getHealth(t *testing.T, port uint) *http.Response {
	t.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	for range 100 {
		if res, err := http.Get(url); err == nil {
			return res
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service did not become healthy")

	return nil
}
