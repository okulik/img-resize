package main

import (
	"context"
	"fmt"
	"log"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/service"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/worker"
)

func main() {
	settings, err := settings.Load()
	if err != nil {
		log.Fatal(err)
	}

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", settings.Service.RedisHost, settings.Service.RedisPort),
		Password: settings.Service.RedisPassword,
		DB:       0,
	})

	cache, err := cache.NewRedisImageCache(client, settings)
	if err != nil {
		log.Panicf("Faild to create image cache: %v", err)
	}

	jobs := queue.New(client, settings)

	// Run a worker in this process until the service stops.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		if err := worker.New(settings, jobs, cache, image.NewResizer(settings)).Run(workerCtx); err != nil {
			log.Printf("worker stopped: %v", err)
		}
	}()

	svc := service.NewService(settings, cache, jobs)
	err = svc.Start()
	stopWorker()
	<-workerDone
	if err != nil {
		log.Fatal(err)
	}
}
