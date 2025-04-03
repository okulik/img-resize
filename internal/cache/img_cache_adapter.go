package cache

import "context"

type ImageCacheAdapter interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Contains(ctx context.Context, key string) bool
	Add(ctx context.Context, key string, value any) bool
}
