package rest_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/okulik/fm-go/internal/cache"
	"github.com/okulik/fm-go/internal/image"
	"github.com/okulik/fm-go/internal/model"
	"github.com/okulik/fm-go/internal/rest"
	"github.com/okulik/fm-go/internal/settings"
)

var json string = `{
	"urls": [
		"https://i.imgur.com/RzW6QSI.jpeg",
		"https://httpstat.us/404"
	],
	"width": 200,
	"height": 0
}`

func TestResizeImage(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(json))
	req, err := http.NewRequest("POST", "/v1/resize?async=false", reader)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	handler := buildResizerHandler()

	testRecorder := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Post("/v1/resize", handler.ResizeImage)
	router.ServeHTTP(testRecorder, req)

	if testRecorder.Code != http.StatusCreated {
		t.Fatalf("unexpected status code: %v", testRecorder.Code)
	}

	if !strings.Contains(testRecorder.Body.String(), "abc123") {
		t.Fatalf("unexpected image id")
	}
}

func TestResizeImageAsync(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(json))
	req, err := http.NewRequest("POST", "/v1/resize?async=true", reader)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	handler := buildResizerHandler()

	testRecorder := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Post("/v1/resize", handler.ResizeImage)
	router.ServeHTTP(testRecorder, req)

	if testRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", testRecorder.Code)
	}

	if !strings.Contains(testRecorder.Body.String(), "def456") {
		t.Fatalf("unexpected image id")
	}
}

func buildResizerHandler() *rest.ResizerHandler {
	settings, _ := settings.Load()
	cache, _ := cache.NewLRUImageCache(1)
	resizer := NewMockResizer(settings, cache)

	return rest.NewResizerHandler(settings, cache, resizer)
}

type mockImageResizer struct {
	settings         *settings.Settings
	cache            cache.ImageCacheAdapter
	resizingProgress *image.ResizingProgress
}

func NewMockResizer(settings *settings.Settings, cache cache.ImageCacheAdapter) image.ImageResizer {
	return &mockImageResizer{
		settings:         settings,
		cache:            cache,
		resizingProgress: image.NewResizingProgress(settings),
	}
}

func (mir *mockImageResizer) Start() {

}

func (mir *mockImageResizer) Shutdown() {

}

func (mir *mockImageResizer) Process(_ *model.ResizeRequest, _ context.Context) ([]model.ResizeResponse, error) {
	resp := make([]model.ResizeResponse, 0, 1)
	resp = append(resp, model.ResizeResponse{Result: "success", ID: "abc123", Cached: false})
	return resp, nil
}

func (mir *mockImageResizer) ProcessAsync(_ *model.ResizeRequest) []model.ResizeResponse {
	resp := make([]model.ResizeResponse, 0, 1)
	resp = append(resp, model.ResizeResponse{Result: "enqueued", ID: "def456", Cached: false})
	return resp
}

func (mir *mockImageResizer) ResizingProgress() *image.ResizingProgress {
	return mir.resizingProgress
}
