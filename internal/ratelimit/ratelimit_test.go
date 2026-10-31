package ratelimit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/ratelimit"
	"github.com/okulik/img-resize/internal/testredis"
)

func TestPrefixKey(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		ipv4Bits int
		want     string
	}{
		{"ipv4", "203.0.113.7", 32, "203.0.113.7/32"},
		{"ipv4 shorter prefix", "203.0.113.7", 24, "203.0.113.0/24"},
		{"ipv6", "2001:db8:1:2:aaaa::1", 32, "2001:db8:1:2::/64"},
		{"ipv6 same prefix", "2001:db8:1:2:bbbb::9", 32, "2001:db8:1:2::/64"},
		{"mapped ipv4", "::ffff:203.0.113.7", 32, "203.0.113.7/32"},
		{"not an ip", "not-an-ip", 32, "not-an-ip"},
		{"ipv4 bits too long", "203.0.113.7", 40, "203.0.113.7/32"},
		{"ipv4 bits negative", "203.0.113.7", -1, "203.0.113.7/32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ratelimit.PrefixKey(tt.ip, tt.ipv4Bits, 64); got != tt.want {
				t.Errorf("unexpected key: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestPrefixKeyInvalidIPv6Bits(t *testing.T) {
	got := ratelimit.PrefixKey("2001:db8:1:2:aaaa::1", 32, 200)

	if want := "2001:db8:1:2:aaaa::1/128"; got != want {
		t.Errorf("unexpected key: got %q want %q", got, want)
	}
}

func clientIPRequest(header string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/resize", nil)
	req.RemoteAddr = "198.51.100.1:5555"
	if header != "" {
		req.Header.Set("Fly-Client-IP", header)
	}
	return req
}

func TestClientIPIgnoresHeaderByDefault(t *testing.T) {
	got := ratelimit.ClientIP(clientIPRequest("203.0.113.9"), false)

	if got != "198.51.100.1" {
		t.Errorf("unexpected ip: %q", got)
	}
}

func TestClientIPUsesHeaderWhenTrusted(t *testing.T) {
	got := ratelimit.ClientIP(clientIPRequest("203.0.113.9"), true)

	if got != "203.0.113.9" {
		t.Errorf("unexpected ip: %q", got)
	}
}

func TestClientIPFallsBackOnUnparsableHeader(t *testing.T) {
	for _, header := range []string{"not-an-ip", "203.0.113.9, 198.51.100.7", "203.0.113.9:5555", " 203.0.113.9"} {
		t.Run(header, func(t *testing.T) {
			got := ratelimit.ClientIP(clientIPRequest(header), true)

			if got != "198.51.100.1" {
				t.Errorf("unexpected ip: %q", got)
			}
		})
	}
}

func TestClientIPUsesIPv6HeaderWhenTrusted(t *testing.T) {
	got := ratelimit.ClientIP(clientIPRequest("2001:db8::1"), true)

	if got != "2001:db8::1" {
		t.Errorf("unexpected ip: %q", got)
	}
}

func TestClientIPFallsBackWithoutHeader(t *testing.T) {
	got := ratelimit.ClientIP(clientIPRequest(""), true)

	if got != "198.51.100.1" {
		t.Errorf("unexpected ip: %q", got)
	}
}

func TestClientIPKeepsUnsplittableRemoteAddr(t *testing.T) {
	req := clientIPRequest("")
	req.RemoteAddr = "pipe"

	if got := ratelimit.ClientIP(req, false); got != "pipe" {
		t.Errorf("unexpected ip: %q", got)
	}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newClock() *clock {
	return &clock{now: time.Unix(1_700_000_040, 0)} // on a minute boundary
}

func TestAllowUpToLimit(t *testing.T) {
	l := ratelimit.New(testredis.Client(t, 5), newClock().Now)
	ctx := context.Background()

	for i := 1; i <= 120; i++ {
		allowed, _, err := l.Allow(ctx, "requests", "p", 1, 120)
		if err != nil || !allowed {
			t.Fatalf("request %d: allowed %v, err %v", i, allowed, err)
		}
	}
	allowed, _, err := l.Allow(ctx, "requests", "p", 1, 120)
	if err != nil || allowed {
		t.Errorf("request 121: allowed %v, err %v", allowed, err)
	}
}

func TestAllowCountsAmount(t *testing.T) {
	l := ratelimit.New(testredis.Client(t, 5), newClock().Now)
	ctx := context.Background()

	allowed, _, err := l.Allow(ctx, "requests", "p", 100, 120)
	if err != nil || !allowed {
		t.Fatalf("first: allowed %v, err %v", allowed, err)
	}
	allowed, _, err = l.Allow(ctx, "requests", "p", 100, 120)
	if err != nil || allowed {
		t.Errorf("second: allowed %v, err %v", allowed, err)
	}
}

func TestAllowResetsNextMinute(t *testing.T) {
	c := newClock()
	l := ratelimit.New(testredis.Client(t, 5), c.Now)
	ctx := context.Background()
	if allowed, _, _ := l.Allow(ctx, "requests", "p", 1, 1); !allowed {
		t.Fatal("first request refused")
	}
	if allowed, _, _ := l.Allow(ctx, "requests", "p", 1, 1); allowed {
		t.Fatal("second request allowed")
	}

	c.now = c.now.Add(time.Minute)

	allowed, _, err := l.Allow(ctx, "requests", "p", 1, 1)
	if err != nil || !allowed {
		t.Errorf("next minute: allowed %v, err %v", allowed, err)
	}
}

func TestAllowReportsRetryAfter(t *testing.T) {
	c := newClock()
	c.now = c.now.Add(30 * time.Second)
	l := ratelimit.New(testredis.Client(t, 5), c.Now)

	_, retryAfter, err := l.Allow(context.Background(), "requests", "p", 1, 1)

	if err != nil || retryAfter != 30*time.Second {
		t.Errorf("retryAfter %v, err %v", retryAfter, err)
	}
}

func TestAllowSharedBetweenLimiters(t *testing.T) {
	client := testredis.Client(t, 5)
	c := newClock()
	a := ratelimit.New(client, c.Now)
	b := ratelimit.New(client, c.Now)
	ctx := context.Background()
	if allowed, _, _ := a.Allow(ctx, "requests", "p", 1, 1); !allowed {
		t.Fatal("first request refused")
	}

	allowed, _, err := b.Allow(ctx, "requests", "p", 1, 1)

	if err != nil || allowed {
		t.Errorf("allowed %v, err %v", allowed, err)
	}
}

func TestAllowSetsKeyWithTTL(t *testing.T) {
	client := testredis.Client(t, 5)
	l := ratelimit.New(client, newClock().Now)
	ctx := context.Background()
	if _, _, err := l.Allow(ctx, "requests", "p", 1, 1); err != nil {
		t.Fatalf("allow: %v", err)
	}

	ttl, err := client.TTL(ctx, "resize:requests:p:28333334").Result()

	if err != nil || ttl <= time.Minute || ttl > 2*time.Minute {
		t.Errorf("ttl %v, err %v", ttl, err)
	}
}
