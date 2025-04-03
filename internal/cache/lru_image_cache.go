package cache

import (
	"context"

	lru "github.com/hashicorp/golang-lru"
)

type LRUImageCache struct {
	*lru.Cache
}

func NewLRUImageCache(size int) (ImageCacheAdapter, error) {
	cache, err := lru.New(size)
	if err != nil {
		return nil, err
	}

	return &LRUImageCache{cache}, nil
}

func NewLRUImageCacheWithCacheImpl(cache *lru.Cache) (ImageCacheAdapter, error) {
	return &LRUImageCache{cache}, nil
}

func (cache *LRUImageCache) Get(_ context.Context, key string) ([]byte, bool) {
	val, ok := cache.Cache.Get(key)
	if val != nil {
		return val.([]byte), ok
	}

	return nil, false
}

func (cache *LRUImageCache) Contains(_ context.Context, key string) bool {
	return cache.Cache.Contains(key)
}

func (cache *LRUImageCache) Add(_ context.Context, key string, value any) bool {
	return cache.Cache.Add(key, value)
}
