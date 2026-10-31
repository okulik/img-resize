package settings

import (
	"fmt"
	"os"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type ServiceSettings struct {
	ImageCacheSize          int           `envconfig:"SVC_IMG_CACHE_SIZE" default:"1024"`
	ImageCacheTTL           time.Duration `envconfig:"SVC_IMG_CACHE_TTL" default:"1h"`
	MaxImageSize            int64         `envconfig:"SVC_MAX_IMG_SIZE" default:"15728640"`
	MaxImageDimension       uint          `envconfig:"SVC_MAX_IMG_DIMENSION" default:"4096"`
	MaxImagePixels          int64         `envconfig:"SVC_MAX_IMG_PIXELS" default:"50000000"`
	MaxImageSourceDimension uint          `envconfig:"SVC_MAX_IMG_SOURCE_DIMENSION" default:"8192"`
	RedisHost               string        `envconfig:"SVC_REDIS_HOST" default:"0.0.0.0"`
	RedisPort               int           `envconfig:"SVC_REDIS_PORT" default:"6379"`
	RedisPassword           string        `envconfig:"SVC_REDIS_PASSWORD"`
	JobStateTTL             time.Duration `envconfig:"SVC_JOB_STATE_TTL" default:"1h"`
	FailedJobTTL            time.Duration `envconfig:"SVC_FAILED_JOB_TTL" default:"5m"`
	JobClaimIdle            time.Duration `envconfig:"SVC_JOB_CLAIM_IDLE" default:"60s"`
	JobReclaimInterval      time.Duration `envconfig:"SVC_JOB_RECLAIM_INTERVAL" default:"15s"`
	WorkerConcurrency       int           `envconfig:"SVC_WORKER_CONCURRENCY" default:"1"`
	JobMaxAttempts          int64         `envconfig:"SVC_JOB_MAX_ATTEMPTS" default:"3"`
	MaxQueueDepth           int64         `envconfig:"SVC_MAX_QUEUE_DEPTH" default:"10000"`
	SyncWaitTimeout         time.Duration `envconfig:"SVC_SYNC_WAIT_TIMEOUT" default:"15s"`
	AllowInternalURLs       bool          `envconfig:"SVC_ALLOW_INTERNAL_URLS" default:"false"`
}

type HttpSettings struct {
	ServerGracefulShutdownTimeout time.Duration `envconfig:"HTTP_SERVER_GRACEFUL_SHUTDOWN_TIMEOUT" default:"10s"`
	ServerPort                    uint          `envconfig:"HTTP_SERVER_PORT" default:"4000"`
	ServerIdleTimeout             time.Duration `envconfig:"HTTP_SERVER_IDLE_TIMEOUT" default:"60s"`
	ServerReadTimeout             time.Duration `envconfig:"HTTP_SERVER_READ_TIMEOUT" default:"10s"`
	ServerWriteTimeout            time.Duration `envconfig:"HTTP_SERVER_WRITE_TIMEOUT" default:"20s"`
	ClientReadTimeout             time.Duration `envconfig:"HTTP_CLIENT_READ_TIMEOUT" default:"10s"`
	ClientUserAgent               string        `envconfig:"HTTP_CLIENT_USER_AGENT" default:"img-resize"`
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
