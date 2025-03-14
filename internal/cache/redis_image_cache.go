package cache

import (
	"context"
	"fmt"
	"log"

	"github.com/okulik/fm-go/internal/settings"
	redis "github.com/redis/go-redis/v9"
)

type RedisImageCache struct {
	*redis.Client
	settings *settings.Settings
}

func NewRedisImageCache(settings *settings.Settings) (ImageCacheAdapter, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", settings.Service.RedisHost, settings.Service.RedisPort),
		Password: "",
		DB:       0,
	})
	return &RedisImageCache{
		Client:   client,
		settings: settings,
	}, nil
}

func (cache *RedisImageCache) Get(ctx context.Context, key string) ([]byte, bool) {
	data, err := cache.Client.Get(ctx, key).Bytes()
	if err != nil {
		log.Printf("error reading from cache: %v", err)
		return nil, false
	}

	return data, true
}

func (cache *RedisImageCache) Contains(ctx context.Context, key string) bool {
	if _, err := cache.Client.Get(ctx, key).Bytes(); err != nil {
		log.Printf("error reading from cache: %v", err)
		return false
	}

	return true
}

func (cache *RedisImageCache) Add(ctx context.Context, key string, data []byte) bool {
	if err := cache.Client.Set(ctx, key, data, cache.settings.Service.ImageCacheTTL).Err(); err != nil {
		log.Printf("error saving to cache: %v", err)
		return false
	}

	return true
}
