package settings_test

import (
	"os"
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/settings"
)

func TestSettingsLoadCacheSizeAndTTLSeparately(t *testing.T) {
	t.Setenv("SVC_IMG_CACHE_SIZE", "2048")
	t.Setenv("SVC_IMG_CACHE_TTL", "2h")

	settings, err := settings.Load()
	if err != nil {
		t.Fatalf("unable to load settings: %v", err)
	}

	if settings.Service.ImageCacheSize != 2048 {
		t.Error("unexpected value for SVC_IMG_CACHE_SIZE")
	}

	if settings.Service.ImageCacheTTL != time.Hour*2 {
		t.Error("unexpected value for SVC_IMG_CACHE_TTL")
	}
}

func TestSettingsLoad(t *testing.T) {
	os.Setenv("HTTP_SERVER_GRACEFUL_SHUTDOWN_TIMEOUT", "123s")
	os.Setenv("HTTP_SERVER_PORT", "8080")
	os.Setenv("HTTP_SERVER_IDLE_TIMEOUT", "120s")
	os.Setenv("HTTP_SERVER_READ_TIMEOUT", "12s")
	os.Setenv("HTTP_SERVER_WRITE_TIMEOUT", "13s")
	os.Setenv("HTTP_CLIENT_READ_TIMEOUT", "14s")
	os.Setenv("HTTP_CLIENT_RETRY_MAX", "9")

	os.Setenv("AUTH_USERNAME", "admin1")
	os.Setenv("AUTH_PASSWORD", "admin2")
	os.Setenv("AUTH_REALM", "localhost3")

	settings, err := settings.Load()
	if err != nil {
		t.Error("unable to load settings")
	}

	if settings.Http.ServerGracefulShutdownTimeout != time.Second*123 {
		t.Error("unexpected value for HTTP_SERVER_GRACEFUL_SHUTDOWN_TIMEOUT")
	}

	if settings.Http.ServerPort != 8080 {
		t.Error("unexpected value for HTTP_SERVER_PORT")
	}

	if settings.Http.ServerIdleTimeout != time.Second*120 {
		t.Error("unexpected value for HTTP_SERVER_IDLE_TIMEOUT")
	}

	if settings.Http.ServerReadTimeout != time.Second*12 {
		t.Error("unexpected value for HTTP_SERVER_READ_TIMEOUT")
	}

	if settings.Http.ServerWriteTimeout != time.Second*13 {
		t.Error("unexpected value for HTTP_SERVER_WRITE_TIMEOUT")
	}

	if settings.Http.ClientReadTimeout != time.Second*14 {
		t.Error("unexpected value for HTTP_CLIENT_READ_TIMEOUT")
	}

	if settings.Auth.Username != "admin1" {
		t.Error("unexpected value for AUTH_USERNAME")
	}

	if settings.Auth.Password != "admin2" {
		t.Error("unexpected value for AUTH_PASSWORD")
	}

	if settings.Auth.Realm != "localhost3" {
		t.Error("unexpected value for AUTH_REALM")
	}
}

func TestSettingsJobReclaimInterval(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"default", "", 15 * time.Second},
		{"override", "30s", 30 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv("SVC_JOB_RECLAIM_INTERVAL", test.env)
			}

			settings, err := settings.Load()
			if err != nil {
				t.Fatalf("unable to load settings: %v", err)
			}

			if settings.Service.JobReclaimInterval != test.want {
				t.Errorf("unexpected reclaim interval: got %v want %v", settings.Service.JobReclaimInterval, test.want)
			}
		})
	}
}

func TestSettingsMaxImageProgressivePixels(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int64
	}{
		{"default", "", 50000000},
		{"override", "6000000", 6000000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv("SVC_MAX_IMG_PROGRESSIVE_PIXELS", test.env)
			}

			settings, err := settings.Load()
			if err != nil {
				t.Fatalf("unable to load settings: %v", err)
			}

			if settings.Service.MaxImageProgressivePixels != test.want {
				t.Errorf("unexpected progressive pixel limit: got %d want %d", settings.Service.MaxImageProgressivePixels, test.want)
			}
		})
	}
}

func TestSettingsLoadRole(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"default", "", "web"},
		{"worker", "worker", "worker"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SVC_ROLE", test.env)
			if test.env == "" {
				os.Unsetenv("SVC_ROLE")
			}

			settings, err := settings.Load()
			if err != nil {
				t.Fatalf("unable to load settings: %v", err)
			}

			if settings.Service.Role != test.want {
				t.Errorf("unexpected role: got %q want %q", settings.Service.Role, test.want)
			}
		})
	}
}

func TestSettingsLoadRejectsUnknownRole(t *testing.T) {
	t.Setenv("SVC_ROLE", "cron")

	if _, err := settings.Load(); err == nil {
		t.Fatal("expected an error for an unknown role")
	}
}

func TestSettingsLoadRequiresAuthOnlyForWeb(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		username string
		password string
		wantErr  bool
	}{
		{"web without username", "web", "", "secret", true},
		{"web without password", "web", "admin", "", true},
		{"web with both", "web", "admin", "secret", false},
		{"worker without either", "worker", "", "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SVC_ROLE", test.role)
			t.Setenv("AUTH_USERNAME", test.username)
			t.Setenv("AUTH_PASSWORD", test.password)

			_, err := settings.Load()
			if (err != nil) != test.wantErr {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
