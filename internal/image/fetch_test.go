package image

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

	resizer := NewResizer(buildFetchSettings(false), nil)

	if _, err := resizer.fetch(context.Background(), source.URL); err == nil {
		t.Error("expected fetching an internal address to fail")
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

	resizer := NewResizer(buildFetchSettings(true), nil)

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

	resizer := NewResizer(buildFetchSettings(false), nil)
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
	resizer := NewResizer(buildFetchSettings(false), nil)

	for _, url := range []string{"file:///etc/passwd", "ftp://example.com/a.jpg", "gopher://example.com/"} {
		if _, err := resizer.fetch(context.Background(), url); err == nil {
			t.Errorf("expected fetching %s to fail", url)
		}
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
