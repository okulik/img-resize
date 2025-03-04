package settings_test

import (
	"os"
	"testing"
	"time"

	"github.com/okulik/fm-go/internal/settings"
)

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
