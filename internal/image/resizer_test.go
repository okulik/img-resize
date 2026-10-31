package image_test

import (
	"bytes"
	"context"
	"errors"
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

func TestProcessSourcePixelLimit(t *testing.T) {
	tests := []struct {
		name      string
		maxPixels int64
		want      string
	}{
		{"at limit", 4096, "success"},
		{"above limit", 4095, "failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, 64, 64))
			settings := buildSettings()
			settings.Service.MaxImagePixels = test.maxPixels
			resizer := image.NewResizer(settings, buildCache(t))

			results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}

			if len(results) != 1 || results[0].Result != test.want {
				t.Errorf("unexpected results: got %+v want %v", results, test.want)
			}
		})
	}
}

func TestProcessSourceDimensionLimit(t *testing.T) {
	tests := []struct {
		name         string
		sourceWidth  int
		sourceHeight int
		want         string
	}{
		{"wide at limit", 100, 10, "success"},
		{"tall at limit", 10, 100, "success"},
		{"wide above limit", 101, 10, "failure"},
		{"tall above limit", 10, 101, "failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, test.sourceWidth, test.sourceHeight))
			settings := buildSettings()
			settings.Service.MaxImageSourceDimension = 100
			resizer := image.NewResizer(settings, buildCache(t))

			results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}

			if len(results) != 1 || results[0].Result != test.want {
				t.Errorf("unexpected results: got %+v want %v", results, test.want)
			}
		})
	}
}

func TestProcessOutputDimensionLimit(t *testing.T) {
	tests := []struct {
		name          string
		sourceWidth   int
		sourceHeight  int
		width, height uint
		want          string
	}{
		{"derived height at limit", 10, 1000, 1, 0, "success"},
		{"derived height above limit", 10, 1000, 2, 0, "failure"},
		{"derived width at limit", 1000, 10, 0, 1, "success"},
		{"derived width above limit", 1000, 10, 0, 2, "failure"},
		{"explicit size ignores aspect ratio", 10, 1000, 50, 50, "success"},
		{"unscaled source at limit", 100, 100, 0, 0, "success"},
		{"unscaled source above limit", 101, 100, 0, 0, "failure"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, test.sourceWidth, test.sourceHeight))
			settings := buildSettings()
			settings.Service.MaxImageDimension = 100
			resizer := image.NewResizer(settings, buildCache(t))

			results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: test.width, Height: test.height}, context.Background())
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}

			if len(results) != 1 || results[0].Result != test.want {
				t.Errorf("unexpected results: got %+v want %v", results, test.want)
			}
		})
	}
}

func TestProcessRejectsImageWithZeroHeight(t *testing.T) {
	data := buildJPEG(t, 64, 64)
	frameHeader := bytes.Index(data, []byte{0xff, 0xc0})
	if frameHeader < 0 {
		t.Fatal("jpeg frame header not found")
	}
	data[frameHeader+5], data[frameHeader+6] = 0, 0

	source := serveImage(t, data)
	resizer := image.NewResizer(buildSettings(), buildCache(t))

	results, err := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if len(results) != 1 || results[0].Result != "failure" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestProcessFailsWhenSourceStopsResponding(t *testing.T) {
	release := make(chan struct{})
	source := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	t.Cleanup(source.Close)
	defer close(release)

	settings := buildSettings()
	settings.Http.ClientReadTimeout = 50 * time.Millisecond
	resizer := image.NewResizer(settings, buildCache(t))

	done := make(chan []model.ResizeResponse, 1)
	go func() {
		results, _ := resizer.Process(&model.ResizeRequest{URLs: []string{source.URL}, Width: 32}, context.Background())
		done <- results
	}()

	select {
	case results := <-done:
		if len(results) != 1 || results[0].Result != "failure" {
			t.Errorf("unexpected results: %+v", results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not time out")
	}
}

func TestResizeClassifiesFailures(t *testing.T) {
	statusServer := func(code int) func(t *testing.T) string {
		return func(t *testing.T) string {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			t.Cleanup(server.Close)

			return server.URL
		}
	}
	imageServer := func(data func(t *testing.T) []byte) func(t *testing.T) string {
		return func(t *testing.T) string { return serveImage(t, data(t)).URL }
	}
	jpegOf := func(width int, height int) func(t *testing.T) []byte {
		return func(t *testing.T) []byte { return buildJPEG(t, width, height) }
	}

	tests := []struct {
		name      string
		source    func(t *testing.T) string
		configure func(s *settings.Settings)
		width     uint
		ctx       func(t *testing.T) context.Context
		reason    string
		permanent bool
	}{
		{name: "source returns 404", source: statusServer(http.StatusNotFound), reason: image.ReasonFetch, permanent: true},
		{name: "source returns 301 without location", source: statusServer(http.StatusMultipleChoices), reason: image.ReasonFetch, permanent: true},
		{name: "source returns 429", source: statusServer(http.StatusTooManyRequests), reason: image.ReasonFetch},
		{name: "source returns 408", source: statusServer(http.StatusRequestTimeout), reason: image.ReasonFetch},
		{name: "source returns 503", source: statusServer(http.StatusServiceUnavailable), reason: image.ReasonFetch},
		{
			name: "source times out",
			source: func(t *testing.T) string {
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })

				return server.URL
			},
			configure: func(s *settings.Settings) { s.Http.ClientReadTimeout = 50 * time.Millisecond },
			reason:    image.ReasonFetch,
		},
		{
			name:   "caller context cancelled",
			source: imageServer(jpegOf(64, 64)),
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				return ctx
			},
			reason: image.ReasonFetch,
		},
		{name: "body is not a JPEG", source: imageServer(func(*testing.T) []byte { return []byte("not a jpeg") }), reason: image.ReasonInvalid, permanent: true},
		{
			name:      "body exceeds the size limit",
			source:    imageServer(jpegOf(64, 64)),
			configure: func(s *settings.Settings) { s.Service.MaxImageSize = 100 },
			reason:    image.ReasonTooLarge, permanent: true,
		},
		{
			name:      "over the pixel limit",
			source:    imageServer(jpegOf(64, 64)),
			configure: func(s *settings.Settings) { s.Service.MaxImagePixels = 4095 },
			reason:    image.ReasonTooLarge, permanent: true,
		},
		{
			name:      "over the source side limit",
			source:    imageServer(jpegOf(64, 64)),
			configure: func(s *settings.Settings) { s.Service.MaxImageSourceDimension = 63 },
			reason:    image.ReasonTooLarge, permanent: true,
		},
		{
			name:      "derived size over the limit",
			source:    imageServer(jpegOf(64, 64)),
			configure: func(s *settings.Settings) { s.Service.MaxImageDimension = 32 },
			width:     64,
			reason:    image.ReasonTooLarge, permanent: true,
		},
		{
			name:      "internal address",
			source:    imageServer(jpegOf(64, 64)),
			configure: func(s *settings.Settings) { s.Service.AllowInternalURLs = false },
			reason:    image.ReasonFetch, permanent: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := buildSettings()
			if test.configure != nil {
				test.configure(settings)
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx(t)
			}
			width := test.width
			if width == 0 {
				width = 32
			}
			resizer := image.NewResizer(settings, buildCache(t))

			data, err := resizer.Resize(ctx, test.source(t), width, 0)

			var failure *image.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("expected a *image.Failure: got %v (data %d bytes)", err, len(data))
			}
			if failure.Reason != test.reason || failure.Permanent != test.permanent {
				t.Errorf("unexpected failure: got reason %q permanent %v want reason %q permanent %v (%v)",
					failure.Reason, failure.Permanent, test.reason, test.permanent, failure.Err)
			}
		})
	}
}

func TestResizeReturnsResizedImage(t *testing.T) {
	source := serveImage(t, buildJPEG(t, 64, 64))
	resizer := image.NewResizer(buildSettings(), buildCache(t))

	data, err := resizer.Resize(context.Background(), source.URL, 32, 0)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if config.Width != 32 || config.Height != 32 {
		t.Errorf("unexpected size: got %dx%d want 32x32", config.Width, config.Height)
	}
}

type rejectingCache struct{}

func (rejectingCache) Get(context.Context, string) ([]byte, bool) { return nil, false }
func (rejectingCache) Contains(context.Context, string) bool      { return false }
func (rejectingCache) Add(context.Context, string, any) bool      { return false }

func buildSettings() *settings.Settings {
	return &settings.Settings{
		Service: &settings.ServiceSettings{
			AsyncResize:             true,
			ImageResizeTimeout:      time.Second,
			MaxImageSize:            1024 * 1024,
			MaxImagePixels:          1024 * 1024,
			MaxImageDimension:       1024,
			MaxImageSourceDimension: 1024,
			AllowInternalURLs:       true,
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
