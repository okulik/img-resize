package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/rest"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
)

func TestResizeEnqueuesJobs(t *testing.T) {
	f := newFixture(t)

	rec := f.resize(t, "/v1/resize", `{"urls":["https://example.com/a.jpg","https://example.com/b.jpg"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	results := decodeResults(t, rec)
	if len(results) != 2 {
		t.Fatalf("unexpected number of results: %v", len(results))
	}
	for i, url := range []string{"https://example.com/a.jpg", "https://example.com/b.jpg"} {
		want := model.ResizeResponse{Result: "enqueued", ID: model.ImageID(url, 200, 0)}
		if results[i] != want {
			t.Errorf("unexpected result %d: got %+v want %+v", i, results[i], want)
		}
	}
	if depth := f.depth(t); depth != 2 {
		t.Errorf("unexpected queue depth: got %d want 2", depth)
	}
}

func TestResizeReportsCachedImage(t *testing.T) {
	f := newFixture(t)
	url := "https://example.com/a.jpg"
	id := model.ImageID(url, 200, 0)
	f.cache.Add(context.Background(), id, []byte("jpeg"))

	rec := f.resize(t, "/v1/resize", `{"urls":["`+url+`"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	results := decodeResults(t, rec)
	want := model.ResizeResponse{Result: "success", ID: id, Cached: true}
	if len(results) != 1 || results[0] != want {
		t.Errorf("unexpected results: got %+v want [%+v]", results, want)
	}
	if depth := f.depth(t); depth != 0 {
		t.Errorf("unexpected queue depth: got %d want 0", depth)
	}
}

func TestResizeRepeatedURLCreatesOneJob(t *testing.T) {
	f := newFixture(t)
	url := "https://example.com/a.jpg"

	rec := f.resize(t, "/v1/resize", `{"urls":["`+url+`","`+url+`"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	results := decodeResults(t, rec)
	want := model.ResizeResponse{Result: "enqueued", ID: model.ImageID(url, 200, 0)}
	if len(results) != 2 || results[0] != want || results[1] != want {
		t.Errorf("unexpected results: got %+v want two of %+v", results, want)
	}
	if depth := f.depth(t); depth != 1 {
		t.Errorf("unexpected queue depth: got %d want 1", depth)
	}
}

func TestResizeReportsFailedJob(t *testing.T) {
	f := newFixture(t)
	url := "https://example.com/a.jpg"
	id := model.ImageID(url, 200, 0)
	key := "resize:job:" + id
	ctx := context.Background()
	if err := f.client.HSet(ctx, key, "status", "failed", "error", "invalid image").Err(); err != nil {
		t.Fatalf("seeding failed state: %v", err)
	}
	if err := f.client.PExpire(ctx, key, time.Minute).Err(); err != nil {
		t.Fatalf("expiring failed state: %v", err)
	}

	rec := f.resize(t, "/v1/resize", `{"urls":["`+url+`"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	results := decodeResults(t, rec)
	want := model.ResizeResponse{Result: "failure", ID: id}
	if len(results) != 1 || results[0] != want {
		t.Errorf("unexpected results: got %+v want [%+v]", results, want)
	}
	if strings.Contains(rec.Body.String(), "invalid image") {
		t.Errorf("response exposes the failure reason: %s", rec.Body.String())
	}
	if depth := f.depth(t); depth != 0 {
		t.Errorf("unexpected queue depth: got %d want 0", depth)
	}
}

func TestResizeRejectsFullQueue(t *testing.T) {
	f := newFixture(t, func(s *settings.ServiceSettings) { s.MaxQueueDepth = 1 })

	first := f.resize(t, "/v1/resize", `{"urls":["https://example.com/a.jpg"],"width":200}`)
	if first.Code != http.StatusOK {
		t.Fatalf("unexpected status code for the first request: %v", first.Code)
	}

	rec := f.resize(t, "/v1/resize", `{"urls":["https://example.com/b.jpg"],"width":200}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After is not set")
	}
	if depth := f.depth(t); depth != 1 {
		t.Errorf("unexpected queue depth: got %d want 1", depth)
	}
}

func TestResizeReportsUnavailableWhenRedisFails(t *testing.T) {
	f := newFixture(t)
	if err := f.client.Close(); err != nil {
		t.Fatalf("closing client: %v", err)
	}

	rec := f.resize(t, "/v1/resize", `{"urls":["https://example.com/a.jpg"],"width":200}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"service unavailable"}` {
		t.Errorf("unexpected body: %s", got)
	}
}

func TestResizeAcceptsAsyncParameter(t *testing.T) {
	f := newFixture(t)

	rec := f.resize(t, "/v1/resize?async=true", `{"urls":["https://example.com/a.jpg"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	results := decodeResults(t, rec)
	if len(results) != 1 || results[0].Result != "enqueued" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestGetImageServesCachedImage(t *testing.T) {
	f := newFixture(t)
	f.cache.Add(context.Background(), "abc", []byte("jpeg"))

	rec := f.get("/v1/image/abc")

	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Errorf("unexpected response: %d %q", rec.Code, rec.Body.String())
	}
}

func TestGetImageReturnsNotFoundForUncachedImage(t *testing.T) {
	f := newFixture(t)

	rec := f.get("/v1/image/missing")

	if rec.Code != http.StatusNotFound {
		t.Errorf("unexpected status code: %v", rec.Code)
	}
}

func TestResizeImageDimensionLimit(t *testing.T) {
	tests := []struct {
		name   string
		width  uint
		height uint
		want   int
	}{
		{"both within limit", 100, 100, http.StatusOK},
		{"width above limit", 101, 0, http.StatusBadRequest},
		{"height above limit", 0, 101, http.StatusBadRequest},
		{"overflowing width", 4294967295, 0, http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, func(s *settings.ServiceSettings) { s.MaxImageDimension = 100 })
			body := fmt.Sprintf(`{"urls":["https://example.com/a.jpg"],"width":%d,"height":%d}`, test.width, test.height)
			for _, path := range []string{"/v1/resize", "/v1/resize?async=true"} {
				if rec := f.resize(t, path, body); rec.Code != test.want {
					t.Errorf("%s: unexpected status code: got %v want %v", path, rec.Code, test.want)
				}
			}
		})
	}
}

type fixture struct {
	client  *redis.Client
	cache   cache.ImageCacheAdapter
	queue   *queue.Queue
	handler *rest.ResizerHandler
}

func newFixture(t *testing.T, tweaks ...func(*settings.ServiceSettings)) *fixture {
	t.Helper()

	client := testredis.Client(t, 4)
	s := &settings.Settings{
		Service: &settings.ServiceSettings{
			ImageCacheTTL:     time.Hour,
			MaxImageDimension: 4096,
			MaxQueueDepth:     100,
			JobStateTTL:       time.Hour,
			FailedJobTTL:      time.Minute,
		},
	}
	for _, tweak := range tweaks {
		tweak(s.Service)
	}

	imageCache, err := cache.NewRedisImageCache(client, s)
	if err != nil {
		t.Fatalf("building cache: %v", err)
	}
	q := queue.New(client, s)

	return &fixture{client: client, cache: imageCache, queue: q, handler: rest.NewResizerHandler(s, imageCache, q)}
}

func (f *fixture) resize(t *testing.T, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req, err := http.NewRequest("POST", path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	rec := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Post("/v1/resize", f.handler.ResizeImage)
	router.ServeHTTP(rec, req)

	return rec
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Get("/v1/image/{imageID}", f.handler.GetImage)
	router.ServeHTTP(rec, req)

	return rec
}

func (f *fixture) depth(t *testing.T) int64 {
	t.Helper()

	depth, err := f.queue.Depth(context.Background())
	if err != nil {
		t.Fatalf("reading queue depth: %v", err)
	}

	return depth
}

func decodeResults(t *testing.T, rec *httptest.ResponseRecorder) []model.ResizeResponse {
	t.Helper()

	var results []model.ResizeResponse
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	return results
}
