package cache_test

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/okulik/img-resize/internal/cache"
)

func TestLRUImageCacheGet(t *testing.T) {
	c, _ := NewMockCache(1)
	cache, err := cache.NewLRUImageCacheWithCacheImpl(c)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add(context.Background(), "foo", []byte("bar"))

	val, ok := cache.Get(context.Background(), "foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}
}

func TestLRUImageCacheAdd(t *testing.T) {
	cache, err := cache.NewLRUImageCache(2)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add(context.Background(), "foo", []byte("1"))
	cache.Add(context.Background(), "bar", []byte("2"))
	cache.Add(context.Background(), "baz", []byte("3"))

	_, ok := cache.Get(context.Background(), "foo")
	if ok {
		t.Error("add method not replacing older items")
	}

	val, ok := cache.Get(context.Background(), "bar")
	if !ok || string(val) != "2" {
		t.Error("method returning unexpected value")
	}

	val, ok = cache.Get(context.Background(), "baz")
	if !ok || string(val) != "3" {
		t.Error("method returning unexpected value")
	}
}

func TestLRUImageCacheContains(t *testing.T) {
	cache, err := cache.NewLRUImageCache(100)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add(context.Background(), "foo", []byte("bar"))

	if !cache.Contains(context.Background(), "foo") {
		t.Error("get method returning unexpected value")
	}
}

type MockCache lru.Cache

func NewMockCache(size int) (*lru.Cache, error) {
	return lru.New(size)
}

func (mc *MockCache) Get(key any) (value any, ok bool) {
	if key == "foo" {
		return "bar", true
	}

	return nil, false
}
