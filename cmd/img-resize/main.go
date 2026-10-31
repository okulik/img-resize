package main

import (
	"fmt"
	"log"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/metrics"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/ratelimit"
	"github.com/okulik/img-resize/internal/service"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := settings.Load()
	if err != nil {
		return err
	}

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Service.RedisHost, cfg.Service.RedisPort),
		Password: cfg.Service.RedisPassword,
		DB:       0,
	})
	defer func() { _ = client.Close() }()

	cache, err := cache.NewRedisImageCache(client, cfg)
	if err != nil {
		return fmt.Errorf("failed to create image cache: %w", err)
	}

	jobs := queue.New(client, cfg)
	m := metrics.New()

	// Load accepts no other role.
	if cfg.Service.Role == settings.RoleWorker {
		w := worker.New(cfg, jobs, cache, image.NewResizer(cfg), m)
		return worker.NewService(cfg, w, m).Start()
	}

	limiter := ratelimit.New(client, time.Now)

	return service.NewService(cfg, cache, jobs, limiter, m).Start()
}
