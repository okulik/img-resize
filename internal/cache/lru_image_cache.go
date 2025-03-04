package cache

import (
	lru "github.com/hashicorp/golang-lru"
	"github.com/okulik/fm-go/internal/image"
)

type LRUImageCache struct {
	*lru.Cache
}

func NewLRUImageCache(size int) (image.ImageCacheAdapter, error) {
	cache, err := lru.New(size)
	if err != nil {
		return nil, err
	}
	return &LRUImageCache{cache}, nil
}

func (cache *LRUImageCache) Get(key string) ([]byte, bool) {
	val, ok := cache.Cache.Get(key)
	if val != nil {
		return val.([]byte), ok
	}

	return nil, false
}

func (cache *LRUImageCache) Contains(key string) bool {
	return cache.Cache.Contains(key)
}

func (cache *LRUImageCache) Add(key string, data []byte) bool {
	return cache.Cache.Add(key, data)
}
