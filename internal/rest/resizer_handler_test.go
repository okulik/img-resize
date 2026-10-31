package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdimage "image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/rest"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
	"github.com/okulik/img-resize/internal/worker"
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

func TestGetImageReturnsImage(t *testing.T) {
	f := newFixture(t)
	f.cache.Add(context.Background(), "abc", []byte("jpeg"))

	rec := f.get("/v1/image/abc")

	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Errorf("unexpected response: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("unexpected Content-Type: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=900" {
		t.Errorf("unexpected Cache-Control: %q", got)
	}
}

func TestGetImageWhileQueued(t *testing.T) {
	f := newFixture(t)
	f.submit(t, "abc")

	rec := f.get("/v1/image/abc")

	f.requirePending(t, rec, "abc")
}

func TestGetImageWhileProcessing(t *testing.T) {
	f := newFixture(t)
	f.submit(t, "abc")
	if err := f.queue.MarkProcessing(context.Background(), "abc"); err != nil {
		t.Fatalf("marking processing: %v", err)
	}

	rec := f.get("/v1/image/abc")

	f.requirePending(t, rec, "abc")
}

func TestGetImageFailed(t *testing.T) {
	f := newFixture(t)
	if err := f.client.HSet(context.Background(), "resize:job:abc", "status", "failed", "error", "invalid image").Err(); err != nil {
		t.Fatalf("seeding failed state: %v", err)
	}

	rec := f.get("/v1/image/abc")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"result":"failure","id":"abc","error":"invalid image","cached":false}` {
		t.Errorf("unexpected body: %s", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("unexpected Cache-Control: %q", got)
	}
}

func TestGetImageUnknown(t *testing.T) {
	f := newFixture(t)

	rec := f.get("/v1/image/missing")

	if rec.Code != http.StatusNotFound {
		t.Errorf("unexpected status code: %v", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("unexpected Cache-Control: %q", got)
	}
}

func TestGetImageFinishingBetweenReads(t *testing.T) {
	f := newFixture(t)
	// The worker stores the image and drops the job state after the first read.
	f.handler = rest.NewResizerHandler(f.settings, &missesFirstGet{ImageCacheAdapter: f.cache}, f.queue)
	f.cache.Add(context.Background(), "abc", []byte("jpeg"))

	rec := f.get("/v1/image/abc")

	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" {
		t.Errorf("unexpected response: %d %q", rec.Code, rec.Body.String())
	}
}

func TestGetImageRedisDown(t *testing.T) {
	f := newFixture(t)
	if err := f.client.Close(); err != nil {
		t.Fatalf("closing client: %v", err)
	}

	rec := f.get("/v1/image/abc")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"service unavailable"}` {
		t.Errorf("unexpected body: %s", got)
	}
}

func TestResizeRedisDown(t *testing.T) {
	f := newFixture(t)
	// Depth is read first, so only a failing cache check can fail the request.
	f.handler = rest.NewResizerHandler(f.settings, failingCache{f.cache}, f.queue)

	rec := f.resize(t, "/v1/resize", `{"urls":["https://example.com/a.jpg"],"width":200}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"service unavailable"}` {
		t.Errorf("unexpected body: %s", got)
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

func TestSyncReturnsImage(t *testing.T) {
	f := newFixture(t)
	f.listen(t)
	f.startWorker(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = jpeg.Encode(w, stdimage.NewRGBA(stdimage.Rect(0, 0, 8, 8)), nil)
	}))
	t.Cleanup(source.Close)

	rec := f.resize(t, "/v1/resize?sync=true", fmt.Sprintf(`{"urls":[%q],"width":4}`, source.URL))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("unexpected Content-Type: %q", got)
	}
	img, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got := img.Bounds().Dx(); got != 4 {
		t.Errorf("unexpected image width: got %d want 4", got)
	}
}

func TestSyncReturnsCachedImage(t *testing.T) {
	f := newFixture(t)
	url := "https://example.com/a.jpg"
	f.cache.Add(context.Background(), model.ImageID(url, 200, 0), []byte("jpeg"))

	rec := f.resize(t, "/v1/resize?sync=true", `{"urls":["`+url+`"],"width":200}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := rec.Body.String(); got != "jpeg" {
		t.Errorf("unexpected body: %q", got)
	}
	if depth := f.depth(t); depth != 0 {
		t.Errorf("unexpected queue depth: got %d want 0", depth)
	}
}

func TestSyncRequiresOneURL(t *testing.T) {
	f := newFixture(t)

	for _, body := range []string{
		`{"urls":[],"width":200}`,
		`{"urls":["https://example.com/a.jpg","https://example.com/b.jpg"],"width":200}`,
	} {
		if rec := f.resize(t, "/v1/resize?sync=true", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: unexpected status code: got %v want 400", body, rec.Code)
		}
	}
	if depth := f.depth(t); depth != 0 {
		t.Errorf("unexpected queue depth: got %d want 0", depth)
	}
}

func TestSyncTimeoutReturnsID(t *testing.T) {
	f := newFixture(t, func(s *settings.ServiceSettings) { s.SyncWaitTimeout = 100 * time.Millisecond })
	f.listen(t)
	url := "https://example.com/a.jpg"

	rec := f.resize(t, "/v1/resize?sync=true", `{"urls":["`+url+`"],"width":200}`)

	f.requirePending(t, rec, model.ImageID(url, 200, 0))
}

func TestSyncFailureReturnsError(t *testing.T) {
	f := newFixture(t)
	f.listen(t)
	f.startWorker(t)
	url := "ftp://example.com/a.jpg"

	rec := f.resize(t, "/v1/resize?sync=true", `{"urls":["`+url+`"],"width":200}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected status code: %v, body %s", rec.Code, rec.Body.String())
	}
	want := `{"result":"failure","id":"` + model.ImageID(url, 200, 0) + `","error":"fetch failed","cached":false}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("unexpected body: got %s want %s", got, want)
	}
}

func TestSyncReportsKnownFailureWithoutWaiting(t *testing.T) {
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

	rec := f.resize(t, "/v1/resize?sync=true", `{"urls":["`+url+`"],"width":200}`)

	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"error":"invalid image"`) {
		t.Errorf("unexpected response: %v %s", rec.Code, rec.Body.String())
	}
}

func TestSyncRedisDown(t *testing.T) {
	f := newFixture(t)
	url := "https://example.com/a.jpg"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.resize(t, "/v1/resize?sync=true", `{"urls":["`+url+`"],"width":200}`)
	}()
	eventually(t, "the job to be queued", func() bool { return f.depth(t) == 1 })

	// With no listener, the waiter's next recheck of the state fails.
	if err := f.client.Close(); err != nil {
		t.Fatalf("closing client: %v", err)
	}

	select {
	case rec := <-done:
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("unexpected status code: %v", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "service unavailable") {
			t.Errorf("unexpected body: %s", rec.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request did not return")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fixture struct {
	settings *settings.Settings
	client   *redis.Client
	cache    cache.ImageCacheAdapter
	queue    *queue.Queue
	handler  *rest.ResizerHandler
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
			SyncWaitTimeout:   time.Minute,

			// What a worker needs.
			MaxImageSize:            1024 * 1024,
			MaxImagePixels:          1024 * 1024,
			MaxImageSourceDimension: 1024,
			JobClaimIdle:            time.Hour,
			JobReclaimInterval:      time.Hour,
			WorkerConcurrency:       1,
			JobMaxAttempts:          3,
			AllowInternalURLs:       true,
		},
		Http: &settings.HttpSettings{ClientReadTimeout: 10 * time.Second},
	}
	for _, tweak := range tweaks {
		tweak(s.Service)
	}

	imageCache, err := cache.NewRedisImageCache(client, s)
	if err != nil {
		t.Fatalf("building cache: %v", err)
	}
	q := queue.New(client, s)

	return &fixture{settings: s, client: client, cache: imageCache, queue: q, handler: rest.NewResizerHandler(s, imageCache, q)}
}

// listen runs the queue's listener until the test ends.
func (f *fixture) listen(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.queue.Listen(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// startWorker runs a worker until the test ends.
func (f *fixture) startWorker(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	w := worker.New(f.settings, f.queue, f.cache, image.NewResizer(f.settings))
	go func() { _ = w.Run(ctx) }()
	// A worker in a blocked read stops only when the read ends, which closing
	// the client does, so the test does not wait for it.
	t.Cleanup(cancel)
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

// submit enqueues a job with the given ID.
func (f *fixture) submit(t *testing.T, id string) {
	t.Helper()

	if _, err := f.queue.Submit(context.Background(), model.Job{ID: id, URL: "https://example.com/a.jpg", Width: 200}); err != nil {
		t.Fatalf("submitting job: %v", err)
	}
}

func (f *fixture) requirePending(t *testing.T, rec *httptest.ResponseRecorder, id string) {
	t.Helper()

	if rec.Code != http.StatusAccepted {
		t.Fatalf("unexpected status code: %v", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"result":"enqueued","id":"`+id+`","cached":false}` {
		t.Errorf("unexpected body: %s", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("unexpected Cache-Control: %q", got)
	}
}

// failingCache reports an error from every check, as a broken Redis would.
type failingCache struct {
	cache.ImageCacheAdapter
}

func (failingCache) Contains(context.Context, string) (bool, error) {
	return false, errors.New("redis down")
}

// missesFirstGet misses on the first Get, whatever is cached.
type missesFirstGet struct {
	cache.ImageCacheAdapter
	gets atomic.Int64
}

func (c *missesFirstGet) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if c.gets.Add(1) == 1 {
		return nil, false, nil
	}

	return c.ImageCacheAdapter.Get(ctx, key)
}
