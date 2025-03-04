package image

type ImageCacheAdapter interface {
	Get(key string) ([]byte, bool)
	Contains(key string) bool
	Add(key string, data []byte) bool
}
