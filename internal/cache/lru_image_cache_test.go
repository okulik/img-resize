package cache_test

import (
	"testing"

	"github.com/okulik/fm-go/internal/cache"
)

func TestGet(t *testing.T) {
	cache, err := cache.NewLRUImageCache(100)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add("foo", []byte("bar"))

	val, ok := cache.Get("foo")
	if !ok || string(val) != "bar" {
		t.Error("get method returning unexpected value")
	}
}

func TestAdd(t *testing.T) {
	cache, err := cache.NewLRUImageCache(2)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add("foo", []byte("1"))
	cache.Add("bar", []byte("2"))
	cache.Add("baz", []byte("3"))

	_, ok := cache.Get("foo")
	if ok {
		t.Error("add method not replacing older items")
	}

	val, ok := cache.Get("bar")
	if !ok || string(val) != "2" {
		t.Error("method returning unexpected value")
	}

	val, ok = cache.Get("baz")
	if !ok || string(val) != "3" {
		t.Error("method returning unexpected value")
	}
}

func TestContains(t *testing.T) {
	cache, err := cache.NewLRUImageCache(100)
	if err != nil {
		t.Error("error allocating LRUImageCache")
	}
	cache.Add("foo", []byte("bar"))

	if !cache.Contains("foo") {
		t.Error("get method returning unexpected value")
	}
}
