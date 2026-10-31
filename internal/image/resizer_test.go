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

	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/settings"
)

func TestResizeSourcePixelLimit(t *testing.T) {
	tests := []struct {
		name      string
		maxPixels int64
		wantErr   bool
	}{
		{"at limit", 4096, false},
		{"above limit", 4095, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, 64, 64))
			settings := buildSettings()
			settings.Service.MaxImagePixels = test.maxPixels

			_, err := image.NewResizer(settings).Resize(context.Background(), source.URL, 32, 0)

			if (err != nil) != test.wantErr {
				t.Errorf("unexpected error: got %v want error %v", err, test.wantErr)
			}
		})
	}
}

func TestResizeSourceDimensionLimit(t *testing.T) {
	tests := []struct {
		name         string
		sourceWidth  int
		sourceHeight int
		wantErr      bool
	}{
		{"wide at limit", 100, 10, false},
		{"tall at limit", 10, 100, false},
		{"wide above limit", 101, 10, true},
		{"tall above limit", 10, 101, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, test.sourceWidth, test.sourceHeight))
			settings := buildSettings()
			settings.Service.MaxImageSourceDimension = 100

			_, err := image.NewResizer(settings).Resize(context.Background(), source.URL, 32, 0)

			if (err != nil) != test.wantErr {
				t.Errorf("unexpected error: got %v want error %v", err, test.wantErr)
			}
		})
	}
}

func TestResizeOutputDimensionLimit(t *testing.T) {
	tests := []struct {
		name          string
		sourceWidth   int
		sourceHeight  int
		width, height uint
		wantErr       bool
	}{
		{"derived height at limit", 10, 1000, 1, 0, false},
		{"derived height above limit", 10, 1000, 2, 0, true},
		{"derived width at limit", 1000, 10, 0, 1, false},
		{"derived width above limit", 1000, 10, 0, 2, true},
		{"explicit size ignores aspect ratio", 10, 1000, 50, 50, false},
		{"unscaled source at limit", 100, 100, 0, 0, false},
		{"unscaled source above limit", 101, 100, 0, 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := serveImage(t, buildJPEG(t, test.sourceWidth, test.sourceHeight))
			settings := buildSettings()
			settings.Service.MaxImageDimension = 100

			_, err := image.NewResizer(settings).Resize(context.Background(), source.URL, test.width, test.height)

			if (err != nil) != test.wantErr {
				t.Errorf("unexpected error: got %v want error %v", err, test.wantErr)
			}
		})
	}
}

func TestResizeRejectsImageWithZeroHeight(t *testing.T) {
	data := buildJPEG(t, 64, 64)
	frameHeader := bytes.Index(data, []byte{0xff, 0xc0})
	if frameHeader < 0 {
		t.Fatal("jpeg frame header not found")
	}
	data[frameHeader+5], data[frameHeader+6] = 0, 0

	source := serveImage(t, data)

	_, err := image.NewResizer(buildSettings()).Resize(context.Background(), source.URL, 32, 0)

	var failure *image.Failure
	if !errors.As(err, &failure) || failure.Reason != image.ReasonInvalid {
		t.Errorf("unexpected error: %v", err)
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
			resizer := image.NewResizer(settings)

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
	resizer := image.NewResizer(buildSettings())

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

func buildSettings() *settings.Settings {
	return &settings.Settings{
		Service: &settings.ServiceSettings{
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
