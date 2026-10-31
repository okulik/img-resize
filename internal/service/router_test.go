package service_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/ratelimit"
	"github.com/okulik/img-resize/internal/service"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
)

func TestRouterWithResizeEndpoint(t *testing.T) {
	router := buildRouter(t)
	router.Post("/v1/resize", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/v1/resize", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("invalid status returned: got %v want %v", status, http.StatusOK)
	}
}

func TestRouterWithImageEndpoint(t *testing.T) {
	router := buildRouter(t)
	router.Get("/v1/image/{imageID}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/v1/image/3731df6b15afc23322056bf1e234b86b8cdf32f0999eec5ccd3fd6148c8065fd", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("invalid status returned: got %v want %v", status, http.StatusOK)
	}
}

func TestRouterWithMissingEndpoint(t *testing.T) {
	router := buildRouter(t)
	router.Post("/v1/resize", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/v2/resize", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)
	if status := rr.Code; status != http.StatusNotFound {
		t.Errorf("invalid status returned: got %v want %v", status, http.StatusNotFound)
	}
}

func TestRouterWithHealthEndpoint(t *testing.T) {
	router := buildRouter(t)
	router.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("invalid status returned: got %v want %v", status, http.StatusOK)
	}
}

func buildRouter(t *testing.T) *chi.Mux {
	t.Helper()
	client := testredis.Client(t, 6)
	settings, _ := settings.Load()
	cache, _ := cache.NewLRUImageCache(1)

	return service.NewRouter(settings, cache, queue.New(client, settings), ratelimit.New(client, time.Now))
}
