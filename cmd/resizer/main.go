package main

import (
	"log"

	"github.com/okulik/fm-go/internal/cache"
	"github.com/okulik/fm-go/internal/image"
	"github.com/okulik/fm-go/internal/service"
	"github.com/okulik/fm-go/internal/settings"
)

func main() {
	settings, err := settings.Load()
	if err != nil {
		log.Fatal(err)
	}

	//cache, err := cache.NewLRUImageCache(settings.Service.ImageCacheSize)
	cache, err := cache.NewRedisImageCache(settings)
	if err != nil {
		log.Panicf("Faild to create image cache: %v", err)
	}

	resizer := image.NewResizer(settings, cache)
	resizer.Start()

	svc := service.NewService(settings, cache, resizer)
	if err := svc.Start(); err != nil {
		log.Fatal(err)
	}
}
