package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/settings"
)

func TestRedisImageCacheGet(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectGet("resize:image:foo").SetVal("bar")

	cache, err := cache.NewRedisImageCache(db, buildSettings(0))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	val, ok := cache.Get(context.Background(), "foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}
}

func TestRedisImageCacheAdd(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectSet("resize:image:foo", "bar", 0)
	mock.ExpectGet("resize:image:foo").SetVal("bar")
	mock.ExpectGet("resize:image:baz").RedisNil()

	cache, err := cache.NewRedisImageCache(db, buildSettings(0))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	cache.Add(context.Background(), "foo", "bar")
	val, ok := cache.Get(context.Background(), "foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}

	_, ok = cache.Get(context.Background(), "baz")
	if ok {
		t.Error("method expected to return nil for nonexistent key")
	}
}

func TestRedisImageCacheAddWithTTL(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectSet("resize:image:foo", "bar", time.Millisecond)
	mock.ExpectGet("resize:image:foo").SetVal("bar")

	cache, err := cache.NewRedisImageCache(db, buildSettings(time.Millisecond))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	cache.Add(context.Background(), "foo", "bar")
	val, ok := cache.Get(context.Background(), "foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}
}

func TestRedisImageCacheContainsUsesExists(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectExists("resize:image:foo").SetVal(1)

	cache, err := cache.NewRedisImageCache(db, buildSettings(0))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	if !cache.Contains(context.Background(), "foo") {
		t.Error("contains method returning unexpected value")
	}
}

func TestRedisImageCacheContainsMissing(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectExists("resize:image:foo").SetVal(0)

	cache, err := cache.NewRedisImageCache(db, buildSettings(0))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	if cache.Contains(context.Background(), "foo") {
		t.Error("contains method returning true for missing key")
	}
}

func TestNewRedisImageCacheUsesConfiguredPassword(t *testing.T) {
	settings := buildSettings(0)
	settings.Service.RedisPassword = "secret"

	imageCache, err := cache.NewRedisImageCache(nil, settings)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if got := imageCache.(*cache.RedisImageCache).Options().Password; got != "secret" {
		t.Errorf("unexpected redis password: %q", got)
	}
}

func buildSettings(ttl time.Duration) *settings.Settings {
	return &settings.Settings{
		Service: &settings.ServiceSettings{
			ImageCacheTTL: ttl,
		},
		Auth: &settings.AuthSettings{},
		Http: &settings.HttpSettings{},
	}
}
