package settings

import (
	"fmt"
	"os"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type ServiceSettings struct {
	ImageCacheSize     int           `envconfig:"SVC_IMG_CACHE_SIZE" default:"1024"`
	ImageCacheTTL      time.Duration `envconfig:"SVC_IMG_CACHE_SIZE" default:"1h"`
	AsyncResize        bool          `envconfig:"SVC_ASYNC_RESIZE" default:"true"`
	ImageResizeTimeout time.Duration `envconfig:"SVC_IMG_RESIZE_TIMEOUT" default:"5s"`
	MaxImageSize       int64         `envconfig:"SVC_MAX_IMG_SIZE" default:"15728640"`
	RedisHost          string        `envconfig:"SVC_REDIS_HOST" default:"0.0.0.0"`
	RedisPort          int           `envconfig:"SVC_REDIS_PORT" default:"6379"`
}

type HttpSettings struct {
	ServerGracefulShutdownTimeout time.Duration `envconfig:"HTTP_SERVER_GRACEFUL_SHUTDOWN_TIMEOUT" default:"10s"`
	ServerPort                    uint          `envconfig:"HTTP_SERVER_PORT" default:"4000"`
	ServerIdleTimeout             time.Duration `envconfig:"HTTP_SERVER_IDLE_TIMEOUT" default:"60s"`
	ServerReadTimeout             time.Duration `envconfig:"HTTP_SERVER_READ_TIMEOUT" default:"10s"`
	ServerWriteTimeout            time.Duration `envconfig:"HTTP_SERVER_WRITE_TIMEOUT" default:"20s"`
	ClientReadTimeout             time.Duration `envconfig:"HTTP_CLIENT_READ_TIMEOUT" default:"10s"`
	ClientUserAgent               string        `envconfig:"HTTP_CLIENT_USER_AGENT" default:"fm-go"`
}

type AuthSettings struct {
	Username string `envconfig:"AUTH_USERNAME" required:"true"`
	Password string `envconfig:"AUTH_PASSWORD" required:"true"`
	Realm    string `envconfig:"AUTH_REALM" default:"localhost"`
}

type Settings struct {
	Http    *HttpSettings
	Auth    *AuthSettings
	Service *ServiceSettings
}

func Load() (*Settings, error) {
	appEnv := getAppEnv()
	if err := LoadEnvFile(appEnv); err != nil {
		return nil, fmt.Errorf("failed to load env file for '%s': %s", appEnv, err)
	}

	settings := &Settings{}
	if err := envconfig.Process("", settings); err != nil {
		return nil, err
	}

	return settings, nil
}

func getAppEnv() string {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		return "development"
	}
	return appEnv
}
