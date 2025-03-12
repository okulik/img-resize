package cache_test

import (
	"context"
	"testing"

	"github.com/okulik/fm-go/internal/cache"
)

func TestGet(t *testing.T) {
	cache, err := cache.NewLRUImageCache(100)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add(context.Background(), "foo", []byte("bar"))

	val, ok := cache.Get(context.Background(), "foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}
}

func TestAdd(t *testing.T) {
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

func TestContains(t *testing.T) {
	cache, err := cache.NewLRUImageCache(100)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add(context.Background(), "foo", []byte("bar"))

	if !cache.Contains(context.Background(), "foo") {
		t.Error("get method returning unexpected value")
	}
}
