package image_test

import (
	"bytes"
	"context"
	stdimage "image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/settings"
)

func TestProcessReturnsSuccessfulResult(t *testing.T) {
	source := serveImage(t, buildJPEG(t, 64, 64))
	imageCache := buildCache(t)
	resizer := image.NewResizer(buildSettings(), imageCache)

	results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("unexpected number of results: %v", len(results))
	}

	if results[0].Result != "success" {
		t.Errorf("unexpected result: %v", results[0].Result)
	}

	data, ok := imageCache.Get(context.Background(), results[0].ID)
	if !ok {
		t.Fatalf("returned id %q is not cached", results[0].ID)
	}

	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("cached image is not a jpeg: %v", err)
	}

	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
		t.Errorf("unexpected image size: %v", img.Bounds())
	}
}

func TestProcessReturnsResultForEveryURL(t *testing.T) {
	source := serveImage(t, buildJPEG(t, 64, 64))
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	resizer := image.NewResizer(buildSettings(), buildCache(t))

	results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL, missing.URL}, Width: 32}, context.Background())
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("unexpected number of results: %v", len(results))
	}

	if results[0].Result != "success" {
		t.Errorf("unexpected result for first url: %v", results[0].Result)
	}

	if results[1].Result != "failure" {
		t.Errorf("unexpected result for second url: %v", results[1].Result)
	}
}

func TestProcessReportsFailureWhenCacheWriteFails(t *testing.T) {
	source := serveImage(t, buildJPEG(t, 64, 64))
	resizer := image.NewResizer(buildSettings(), rejectingCache{})

	results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("unexpected number of results: %v", len(results))
	}

	if results[0].Result != "failure" {
		t.Errorf("unexpected result: %v", results[0].Result)
	}

	if results[0].ID != "" {
		t.Errorf("unexpected id for an image that was not stored: %v", results[0].ID)
	}
}

type rejectingCache struct{}

func (rejectingCache) Get(context.Context, string) ([]byte, bool) { return nil, false }
func (rejectingCache) Contains(context.Context, string) bool      { return false }
func (rejectingCache) Add(context.Context, string, any) bool      { return false }

func buildSettings() *settings.Settings {
	return &settings.Settings{
		Service: &settings.ServiceSettings{
			AsyncResize:        true,
			ImageResizeTimeout: time.Second,
			MaxImageSize:       1024 * 1024,
		},
		Auth: &settings.AuthSettings{},
		Http: &settings.HttpSettings{ClientReadTimeout: time.Second},
	}
}

func buildCache(t *testing.T) cache.ImageCacheAdapter {
	t.Helper()
	imageCache, err := cache.NewLRUImageCache(16)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	return imageCache
}

func buildJPEG(t *testing.T, width int, height int) []byte {
	t.Helper()
	data := bytes.Buffer{}
	if err := jpeg.Encode(&data, stdimage.NewRGBA(stdimage.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	return data.Bytes()
}

func serveImage(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)

	return server
}
