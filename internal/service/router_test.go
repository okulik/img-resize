package service_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chi "github.com/go-chi/chi/v5"
	"github.com/okulik/fm-go/internal/cache"
	"github.com/okulik/fm-go/internal/image"
	"github.com/okulik/fm-go/internal/service"
	"github.com/okulik/fm-go/internal/settings"
)

func TestRouterWithResizeEndpoint(t *testing.T) {
	router := buildRouter()
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
	router := buildRouter()
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
	router := buildRouter()
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
	router := buildRouter()
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

func buildRouter() *chi.Mux {
	settings, _ := settings.Load()
	cache, _ := cache.NewLRUImageCache(1)
	resizer := image.NewResizer(settings, cache)

	return service.NewRouter(settings, cache, resizer)
}
