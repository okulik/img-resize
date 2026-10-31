package image

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/settings"
)

func TestFetchBlocksInternalAddress(t *testing.T) {
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(source.Close)

	resizer := NewResizer(buildFetchSettings(false))

	_, err := resizer.fetch(context.Background(), source.URL)
	if !errors.Is(err, errInternalAddress) {
		t.Errorf("expected an internal address error: got %v", err)
	}

	if got := requests.Load(); got != 0 {
		t.Errorf("internal address received %d requests", got)
	}
}

func TestFetchAllowsInternalAddressWhenConfigured(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	t.Cleanup(source.Close)

	resizer := NewResizer(buildFetchSettings(true))

	data, err := resizer.fetch(context.Background(), source.URL)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if string(data) != "image" {
		t.Errorf("unexpected data: %q", data)
	}
}

func TestFetchBlocksRedirectToInternalAddress(t *testing.T) {
	var requests atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(internal.Close)

	resizer := NewResizer(buildFetchSettings(false))
	// Answers for a public host without dialing, so that the redirect target
	// is the first address that goes through the real transport.
	resizer.httpClient.Transport = redirectingTransport{
		publicHost: "images.example.com",
		location:   internal.URL,
		next:       resizer.httpClient.Transport,
	}

	if _, err := resizer.fetch(context.Background(), "https://images.example.com/a.jpg"); err == nil {
		t.Error("expected a redirect to an internal address to fail")
	}

	if got := requests.Load(); got != 0 {
		t.Errorf("internal address received %d requests", got)
	}
}

func TestFetchRejectsUnsupportedScheme(t *testing.T) {
	resizer := NewResizer(buildFetchSettings(false))

	for _, url := range []string{"file:///etc/passwd", "ftp://example.com/a.jpg", "gopher://example.com/"} {
		if _, err := resizer.fetch(context.Background(), url); err == nil {
			t.Errorf("expected fetching %s to fail", url)
		}
	}
}

func TestFetchReadsLargeBodyWithoutCopying(t *testing.T) {
	const size = 8 << 20
	body := make([]byte, size)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = w.Write(body)
	}))
	t.Cleanup(source.Close)

	settings := buildFetchSettings(true)
	settings.Service.MaxImageSize = 2 * size
	resizer := NewResizer(settings)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := resizer.fetch(context.Background(), source.URL)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if len(data) != size {
		t.Errorf("unexpected size: got %d want %d", len(data), size)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > size+size/4 {
		t.Errorf("fetching %d bytes allocated %d bytes", size, allocated)
	}
}

func TestFetchSizeLimit(t *testing.T) {
	writeBody := func(size int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, size)) }
	}
	withContentLength := func(next http.HandlerFunc, size int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(size))
			next(w, r)
		}
	}
	chunked := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			next(w, r)
		}
	}

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantLen int
		reason  string
	}{
		{name: "empty body", handler: writeBody(0)},
		{name: "at limit", handler: withContentLength(writeBody(1024), 1024), wantLen: 1024},
		{name: "one over limit", handler: withContentLength(writeBody(1025), 1025), reason: ReasonTooLarge},
		{name: "far over limit", handler: withContentLength(writeBody(4096), 4096), reason: ReasonTooLarge},
		{name: "at limit without length", handler: chunked(writeBody(1024)), wantLen: 1024},
		{name: "over limit without length", handler: chunked(writeBody(1025)), reason: ReasonTooLarge},
		{name: "shorter than its length", handler: withContentLength(writeBody(100), 200), reason: ReasonFetch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := httptest.NewServer(test.handler)
			t.Cleanup(source.Close)

			data, err := NewResizer(buildFetchSettings(true)).fetch(context.Background(), source.URL)

			if test.reason == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if len(data) != test.wantLen {
					t.Errorf("unexpected size: got %d want %d", len(data), test.wantLen)
				}

				return
			}

			var failure *Failure
			if !errors.As(err, &failure) || failure.Reason != test.reason {
				t.Errorf("unexpected error: got %v want reason %q", err, test.reason)
			}
		})
	}
}

func TestIsInternalAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"127.0.0.1:80", true},
		{"10.1.2.3:80", true},
		{"172.16.0.1:80", true},
		{"172.31.255.255:80", true},
		{"192.168.1.1:80", true},
		{"169.254.169.254:80", true},
		{"100.64.0.1:80", true},
		{"100.127.255.255:80", true},
		{"0.0.0.0:80", true},
		{"0.1.2.3:80", true},
		{"224.0.0.1:80", true},
		{"240.0.0.1:80", true},
		{"255.255.255.255:80", true},
		{"[::]:80", true},
		{"[::1]:80", true},
		{"[fe80::1]:80", true},
		{"[fe80::1%eth0]:80", true},
		{"[fc00::1]:80", true},
		{"[fd00:ec2::254]:80", true},
		{"[ff02::1]:80", true},
		{"[::ffff:127.0.0.1]:80", true},
		{"[::ffff:169.254.169.254]:80", true},
		{"[64:ff9b::7f00:1]:80", true},
		{"[2002:7f00:1::]:80", true},
		{"not-an-address", true},
		{"8.8.8.8:443", false},
		{"1.1.1.1:443", false},
		{"172.32.0.1:443", false},
		{"100.128.0.1:443", false},
		{"[2606:4700:4700::1111]:443", false},
		{"[::ffff:8.8.8.8]:443", false},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := isInternalAddress(test.address); got != test.want {
				t.Errorf("got %v want %v", got, test.want)
			}
		})
	}
}

type redirectingTransport struct {
	publicHost string
	location   string
	next       http.RoundTripper
}

func (rt redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != rt.publicHost {
		return rt.next.RoundTrip(req)
	}

	return &http.Response{
		Status:     "302 Found",
		StatusCode: http.StatusFound,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Location": []string{rt.location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func buildFetchSettings(allowInternalURLs bool) *settings.Settings {
	return &settings.Settings{
		Service: &settings.ServiceSettings{
			AllowInternalURLs: allowInternalURLs,
			MaxImageSize:      1024,
		},
		Auth: &settings.AuthSettings{},
		Http: &settings.HttpSettings{ClientReadTimeout: time.Second},
	}
}
