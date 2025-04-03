package service

import (
	"log"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/rest"
	"github.com/okulik/img-resize/internal/settings"
)

const (
	healthPath string = "/health"
	v1Path     string = "/v1"
)

func NewRouter(settings *settings.Settings, imageCache cache.ImageCacheAdapter, resizer image.ImageResizer) *chi.Mux {
	r := chi.NewRouter()
	r.Use(loggingMiddleware)
	r.Get(healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Mount(v1Path, createV1Router(settings, imageCache, resizer))

	return r
}

func createV1Router(settings *settings.Settings, imageCache cache.ImageCacheAdapter, resizer image.ImageResizer) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.BasicAuth(settings.Auth.Realm, map[string]string{settings.Auth.Username: settings.Auth.Password}))
	resizerHandler := rest.NewResizerHandler(settings, imageCache, resizer)
	r.Post("/resize", resizerHandler.ResizeImage)
	r.Get("/image/{imageID}", resizerHandler.GetImage)

	return r
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s\n", r.Method, r.RequestURI)
		next.ServeHTTP(w, r)
	})
}
