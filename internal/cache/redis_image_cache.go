package cache

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/okulik/img-resize/internal/settings"
	redis "github.com/redis/go-redis/v9"
)

const keyPrefix = "resize:image:"

type RedisImageCache struct {
	*redis.Client
	settings *settings.Settings
}

func NewRedisImageCache(client *redis.Client, settings *settings.Settings) (ImageCacheAdapter, error) {
	if client == nil {
		client = redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%d", settings.Service.RedisHost, settings.Service.RedisPort),
			Password: settings.Service.RedisPassword,
			DB:       0,
		})
	}
	return &RedisImageCache{
		Client:   client,
		settings: settings,
	}, nil
}

func (cache *RedisImageCache) Get(ctx context.Context, key string) ([]byte, bool) {
	data, err := cache.Client.Get(ctx, keyPrefix+key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("error reading from cache: %v", err)
		}
		return nil, false
	}

	return data, true
}

func (cache *RedisImageCache) Contains(ctx context.Context, key string) bool {
	n, err := cache.Client.Exists(ctx, keyPrefix+key).Result()
	if err != nil {
		log.Printf("error reading from cache: %v", err)
		return false
	}

	return n > 0
}

func (cache *RedisImageCache) Add(ctx context.Context, key string, value any) bool {
	if err := cache.Client.Set(ctx, keyPrefix+key, value, cache.settings.Service.ImageCacheTTL).Err(); err != nil {
		log.Printf("error saving to cache: %v", err)
		return false
	}

	return true
}
