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
	mock.ExpectGet("foo").SetVal("bar")

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
	mock.ExpectSet("foo", "bar", 0)
	mock.ExpectGet("foo").SetVal("bar")
	mock.ExpectGet("baz").RedisNil()

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
	mock.ExpectSet("foo", "bar", time.Millisecond)
	mock.ExpectGet("foo").SetVal("bar")

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

func TestRedisImageCacheContains(t *testing.T) {
	db, mock := redismock.NewClientMock()
	mock.ExpectGet("foo").SetVal("bar")

	cache, err := cache.NewRedisImageCache(db, buildSettings(0))
	if err != nil {
		t.Error("error allocating RedisImageCache")
	}

	if !cache.Contains(context.Background(), "foo") {
		t.Error("get method returning unexpected value")
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
