package cache

import "context"

type ImageCacheAdapter interface {
	// Get returns the cached value. A miss is false with a nil error, and only
	// a failure of the backing store is an error.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Contains reports whether key is cached, with the same error semantics.
	Contains(ctx context.Context, key string) (bool, error)
	Add(ctx context.Context, key string, value any) bool
}
