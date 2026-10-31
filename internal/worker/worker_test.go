package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	stdimage "image"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/testredis"
)

const (
	// timeout bounds every wait. It is far above what any test needs, so a slow
	// machine fails nothing.
	timeout = 30 * time.Second
	// shortIdle and the reclaim interval below make a retry happen quickly.
	shortIdle = 50 * time.Millisecond
	// noRetry keeps a job from being reclaimed during a test.
	noRetry = time.Hour
)

var errInjected = errors.New("injected redis error")

func TestWorkerStoresResizedImage(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)
	if n := f.depth(t); n != 0 {
		t.Errorf("unexpected depth: %d", n)
	}
}

func TestWorkerSkipsCachedImage(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)
	if !f.cache.Add(context.Background(), job.ID, buildJPEG(t, 32, 32)) {
		t.Fatal("seeding the cache failed")
	}

	f.start(t)

	f.waitFinished(t, job)
	if n := src.requests.Load(); n != 0 {
		t.Errorf("unexpected source requests: %d", n)
	}
}

func TestWorkerRecordsPermanentFailure(t *testing.T) {
	f := newFixture(t, shortIdle)
	src := newSource(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	job := f.submit(t, src.URL)

	f.start(t)

	state := f.waitFailed(t, job)
	if state.Error != "fetch failed" {
		t.Errorf("unexpected error: %q", state.Error)
	}
	if n := src.requests.Load(); n != 1 {
		t.Errorf("unexpected source requests: %d", n)
	}
	if n := f.depth(t); n != 0 {
		t.Errorf("unexpected depth: %d", n)
	}
}

func TestWorkerRetriesTransientFailure(t *testing.T) {
	f := newFixture(t, shortIdle)
	data := buildJPEG(t, 64, 64)
	src := newSource(t, func(n int64, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(data)
	})
	job := f.submit(t, src.URL)

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)
	if n := src.requests.Load(); n != 2 {
		t.Errorf("unexpected source requests: %d", n)
	}
}

func TestWorkerGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFixture(t, shortIdle)
	src := newSource(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	job := f.submit(t, src.URL)

	f.start(t)

	state := f.waitFailed(t, job)
	if state.Error != "fetch failed" {
		t.Errorf("unexpected error: %q", state.Error)
	}
	if n := src.requests.Load(); n != 3 {
		t.Errorf("unexpected source requests: %d", n)
	}
	if n := f.depth(t); n != 0 {
		t.Errorf("unexpected depth: %d", n)
	}
}

func TestWorkerRecoversJobFromDeadConsumer(t *testing.T) {
	f := newFixture(t, shortIdle)
	src := imageSource(t)
	job := f.submitToDeadConsumer(t, src.URL)

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)
}

func TestWorkerCompletesJobWithoutState(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)
	if err := f.client.Del(context.Background(), "resize:job:"+job.ID).Err(); err != nil {
		t.Fatalf("deleting state: %v", err)
	}

	r := f.start(t)

	f.waitForImage(t, job)
	f.waitFinished(t, job)
	if n := f.depth(t); n != 0 {
		t.Errorf("unexpected depth: %d", n)
	}
	r.stop(t)
}

func TestWorkerRetriesWhenCacheWriteFails(t *testing.T) {
	f := newFixture(t, shortIdle)
	rejecting := &rejectsFirstAdd{ImageCacheAdapter: f.cache}
	f.cache = rejecting
	src := imageSource(t)
	job := f.submit(t, src.URL)

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)
	if n := rejecting.adds.Load(); n != 2 {
		t.Errorf("unexpected cache writes: %d", n)
	}
	if n := src.requests.Load(); n != 2 {
		t.Errorf("unexpected source requests: %d", n)
	}
}

func TestWorkerFinishesCurrentJobOnShutdown(t *testing.T) {
	f := newFixture(t, noRetry)
	src, gate := gatedSource(t)
	job := f.submit(t, src.URL)

	r := f.start(t)
	gate.waitStarted(t)
	r.cancel()

	r.requireRunningFor(t, 200*time.Millisecond)

	gate.open()
	r.stop(t)

	f.requireResized(t, job)
	f.requireFinished(t, job)
}

func TestWorkerFinishesJobClaimedDuringShutdown(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)

	r := f.prepare(t)
	f.client.AddHook(hook{after: func(cmd redis.Cmder) {
		if read, ok := cmd.(*redis.XStreamSliceCmd); ok && len(read.Val()) > 0 {
			r.cancel()
		}
	}})
	r.launch()

	r.wait(t)

	f.requireResized(t, job)
	f.requireFinished(t, job)
}

func TestWorkerClaimsNoMoreJobsOnShutdown(t *testing.T) {
	f := newFixture(t, shortIdle, func(s *settings.ServiceSettings) { s.WorkerConcurrency = 2 })
	src, gate := gatedSource(t)
	first := f.submitToDeadConsumer(t, src.URL+"/1")
	second := f.submitToDeadConsumer(t, src.URL+"/2")

	r := f.start(t)
	gate.waitStarted(t)
	r.cancel()

	r.requireRunningFor(t, 200*time.Millisecond)

	gate.open()
	r.stop(t)

	f.requireResized(t, first)
	if got, want := src.paths(), []string{"/1"}; !slices.Equal(got, want) {
		t.Errorf("unexpected source requests: got %v want %v", got, want)
	}

	// The job stays with its dead consumer, without having lost an attempt.
	pending, err := f.client.XPendingExt(context.Background(), &redis.XPendingExtArgs{
		Stream: "resize:jobs", Group: "resize-workers", Start: "-", End: "+", Count: 10,
	}).Result()
	if err != nil {
		t.Fatalf("reading pending jobs: %v", err)
	}
	if len(pending) != 1 || pending[0].Consumer != "dead" || pending[0].RetryCount != 1 {
		t.Errorf("unexpected pending jobs: %+v", pending)
	}
	if !f.hasState(t, second) {
		t.Error("second job lost its state")
	}
}

func TestWorkerSurvivesRedisErrors(t *testing.T) {
	tests := []struct {
		name  string
		start func(t *testing.T, f *fixture) *run
	}{
		{
			name: "closed before Run",
			start: func(t *testing.T, f *fixture) *run {
				_ = f.client.Close()
				return f.start(t)
			},
		},
		{
			name: "closed during Run",
			start: func(t *testing.T, f *fixture) *run {
				r := f.start(t)
				eventually(t, "the consumer group", func() bool {
					groups, err := f.client.XInfoGroups(context.Background(), "resize:jobs").Result()
					return err == nil && len(groups) == 1
				})
				_ = f.client.Close()
				return r
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, noRetry)
			r := test.start(t, f)

			r.requireRunningFor(t, 1500*time.Millisecond)

			r.stop(t)
		})
	}
}

func TestWorkerRetriesGroupSetupUntilRedisIsUp(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)
	f.client.AddHook(failFirst("xgroup", 1))

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)
}

func TestWorkerRecreatesGroupWhenStreamIsLost(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	first := f.submit(t, src.URL+"/1")

	f.start(t)
	f.waitFinished(t, first)

	if err := f.client.Del(context.Background(), "resize:jobs").Err(); err != nil {
		t.Fatalf("deleting stream: %v", err)
	}
	second := f.submit(t, src.URL+"/2")

	f.waitFinished(t, second)
	f.requireResized(t, second)
}

func TestWorkerKeepsReadingAfterReadError(t *testing.T) {
	f := newFixture(t, noRetry)
	src := imageSource(t)
	job := f.submit(t, src.URL)

	var mu sync.Mutex
	var reads []time.Time
	f.client.AddHook(hook{before: func(cmd redis.Cmder) error {
		if cmd.Name() != "xreadgroup" {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		reads = append(reads, time.Now())
		if len(reads) == 1 {
			return errInjected
		}
		return nil
	}})

	f.start(t)

	f.waitFinished(t, job)
	f.requireResized(t, job)

	mu.Lock()
	defer mu.Unlock()
	if len(reads) < 2 {
		t.Fatalf("unexpected reads: %d", len(reads))
	}
	if gap := reads[1].Sub(reads[0]); gap < time.Second {
		t.Errorf("read retried after only %v", gap)
	}
}

func TestWorkerRunsReclaimedJobsWhenReclaimReportsError(t *testing.T) {
	f := newFixture(t, shortIdle)
	src := imageSource(t)
	first := f.submitToDeadConsumer(t, src.URL+"/1")
	second := f.submitToDeadConsumer(t, src.URL+"/2")

	// The first job's lookup fails, so Reclaim claims again and returns the
	// second job together with an error. No later claim can run it instead.
	var claims, lookups atomic.Int64
	f.client.AddHook(hook{before: func(cmd redis.Cmder) error {
		switch cmd.Name() {
		case "xautoclaim":
			if claims.Add(1) > 2 {
				return errInjected
			}
		case "xpending":
			if lookups.Add(1) == 1 {
				return errInjected
			}
		}
		return nil
	}})

	f.start(t)

	f.waitFinished(t, second)
	f.requireResized(t, second)
	if f.cache.Contains(context.Background(), first.ID) {
		t.Error("first job ran, so the test no longer covers a partial reclaim")
	}
}

func TestWorkerClaimsOnlyJobsItCanStart(t *testing.T) {
	f := newFixture(t, shortIdle, func(s *settings.ServiceSettings) { s.WorkerConcurrency = 2 })
	data := buildJPEG(t, 64, 64)

	// busy counts slots seen in use elsewhere. It can miss one around a read or
	// a fetch, but it never counts a free one.
	var busy atomic.Int64
	src := newSource(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		busy.Add(1)
		defer busy.Add(-1)
		_, _ = w.Write(data)
	})
	first := f.submitToDeadConsumer(t, src.URL+"/1")
	second := f.submitToDeadConsumer(t, src.URL+"/2")

	var mu sync.Mutex
	claims := map[string]int{}
	var claimed, slotless, waiting int64
	f.client.AddHook(hook{
		before: func(cmd redis.Cmder) error {
			switch cmd.Name() {
			case "xreadgroup":
				busy.Add(1)
			case "xautoclaim":
				if busy.Load() >= 2 {
					mu.Lock()
					defer mu.Unlock()
					slotless++
				}
			}
			return nil
		},
		after: func(cmd redis.Cmder) {
			switch read := cmd.(type) {
			case *redis.XStreamSliceCmd:
				busy.Add(-1)
			case *redis.XAutoClaimCmd:
				msgs, _ := read.Val()
				mu.Lock()
				defer mu.Unlock()
				for _, msg := range msgs {
					claims[msg.ID]++
					claimed++
				}
				waiting = max(waiting, claimed-src.requests.Load())
			}
		},
	})

	f.start(t)

	f.waitFinished(t, first)
	f.waitFinished(t, second)

	mu.Lock()
	defer mu.Unlock()
	if len(claims) != 2 {
		t.Errorf("unexpected claimed jobs: %v", claims)
	}
	for id, n := range claims {
		if n != 1 {
			t.Errorf("job %s claimed %d times", id, n)
		}
	}
	if slotless != 0 {
		t.Errorf("claimed %d times with no slot free", slotless)
	}
	if waiting != 1 {
		t.Errorf("unexpected most claimed jobs not yet started: %d", waiting)
	}
}

func TestWorkerReclaimsEveryIdleJobInOneTick(t *testing.T) {
	const interval = 3 * time.Second

	f := newFixture(t, shortIdle, func(s *settings.ServiceSettings) { s.JobReclaimInterval = interval })
	src := imageSource(t)
	first := f.submitToDeadConsumer(t, src.URL+"/1")
	second := f.submitToDeadConsumer(t, src.URL+"/2")

	var mu sync.Mutex
	var claimedAt []time.Time
	f.client.AddHook(hook{after: func(cmd redis.Cmder) {
		if claim, ok := cmd.(*redis.XAutoClaimCmd); ok {
			if msgs, _ := claim.Val(); len(msgs) > 0 {
				mu.Lock()
				defer mu.Unlock()
				claimedAt = append(claimedAt, time.Now())
			}
		}
	}})

	f.start(t)

	f.waitFinished(t, first)
	f.waitFinished(t, second)

	mu.Lock()
	defer mu.Unlock()
	if len(claimedAt) != 2 {
		t.Fatalf("unexpected claims: %d", len(claimedAt))
	}
	if gap := claimedAt[1].Sub(claimedAt[0]); gap >= interval {
		t.Errorf("second job waited %v for another tick", gap)
	}
}

func TestWorkerLetsReadsRunBetweenReclaimedJobs(t *testing.T) {
	f := newFixture(t, shortIdle)
	src, gate := gatedSource(t)
	f.submitToDeadConsumer(t, src.URL+"/reclaimed/1")
	f.submitToDeadConsumer(t, src.URL+"/reclaimed/2")

	f.start(t)
	gate.waitStarted(t)
	job := f.submit(t, src.URL+"/new")
	gate.open()

	f.waitFinished(t, job)
	want := []string{"/reclaimed/1", "/new"}
	if got := src.paths(); len(got) < 2 || !slices.Equal(got[:2], want) {
		t.Errorf("unexpected source requests: got %v want them to start with %v", got, want)
	}
}

func TestWorkerNeverExceedsConcurrency(t *testing.T) {
	f := newFixture(t, shortIdle, func(s *settings.ServiceSettings) { s.WorkerConcurrency = 1 })
	data := buildJPEG(t, 64, 64)

	var inFlight, peak atomic.Int64
	src := newSource(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := peak.Load()
			if n <= seen || peak.CompareAndSwap(seen, n) {
				break
			}
		}
		// Give a second job the time to start if anything allows it to.
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write(data)
	})

	var jobs []model.Job
	for i := range 3 {
		jobs = append(jobs, f.submitToDeadConsumer(t, fmt.Sprintf("%s/reclaimed/%d", src.URL, i)))
	}
	for i := range 3 {
		jobs = append(jobs, f.submit(t, fmt.Sprintf("%s/new/%d", src.URL, i)))
	}

	f.start(t)

	for _, job := range jobs {
		f.waitFinished(t, job)
	}
	if n := peak.Load(); n != 1 {
		t.Errorf("unexpected peak of concurrent fetches: %d", n)
	}
}

func TestWorkerLeavesNoGoroutines(t *testing.T) {
	f := newFixture(t, shortIdle, func(s *settings.ServiceSettings) { s.WorkerConcurrency = 3 })
	src := imageSource(t)
	job := f.submit(t, src.URL)

	r := f.start(t)
	f.waitFinished(t, job)
	r.stop(t)

	// A goroutine's stack names the Worker method it runs or was started by.
	eventually(t, "worker goroutines to exit", func() bool {
		buf := make([]byte, 1<<20)
		return !bytes.Contains(buf[:runtime.Stack(buf, true)], []byte("(*Worker)."))
	})
}

func TestConsumerName(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("reading hostname: %v", err)
	}

	tests := []struct {
		name      string
		machineID string
		want      string
	}{
		{"on a Fly machine", "3d8d9d1b", "3d8d9d1b"},
		{"elsewhere", "", hostname},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("FLY_MACHINE_ID", test.machineID)

			if got := consumerName(); got != test.want {
				t.Errorf("unexpected consumer name: got %q want %q", got, test.want)
			}
		})
	}
}

// fixture is a Redis-backed queue and cache and a real resizer.
type fixture struct {
	client   *redis.Client
	settings *settings.Settings
	queue    *queue.Queue
	cache    cache.ImageCacheAdapter
	resizer  *image.Resizer
}

func newFixture(t *testing.T, claimIdle time.Duration, tweaks ...func(*settings.ServiceSettings)) *fixture {
	t.Helper()

	client := testredis.Client(t, 3)
	s := &settings.Settings{
		Service: &settings.ServiceSettings{
			ImageCacheTTL:           time.Hour,
			MaxImageSize:            1024 * 1024,
			MaxImagePixels:          1024 * 1024,
			MaxImageDimension:       1024,
			MaxImageSourceDimension: 1024,
			JobStateTTL:             time.Hour,
			FailedJobTTL:            time.Hour,
			JobClaimIdle:            claimIdle,
			JobReclaimInterval:      10 * time.Millisecond,
			WorkerConcurrency:       1,
			JobMaxAttempts:          3,
			AllowInternalURLs:       true,
		},
		Http: &settings.HttpSettings{ClientReadTimeout: timeout},
	}
	for _, tweak := range tweaks {
		tweak(s.Service)
	}

	imageCache, err := cache.NewRedisImageCache(client, s)
	if err != nil {
		t.Fatalf("building cache: %v", err)
	}

	return &fixture{
		client:   client,
		settings: s,
		queue:    queue.New(client, s),
		cache:    imageCache,
		resizer:  image.NewResizer(s),
	}
}

func (f *fixture) submit(t *testing.T, url string) model.Job {
	t.Helper()

	job := model.Job{ID: model.ImageID(url, 32, 0), URL: url, Width: 32}
	res, err := f.queue.Submit(context.Background(), job)
	if err != nil {
		t.Fatalf("submitting job: %v", err)
	}
	if res != queue.Created {
		t.Fatalf("unexpected submission: %v", res)
	}

	return job
}

// submitToDeadConsumer leaves a job pending on a consumer that never finishes it.
func (f *fixture) submitToDeadConsumer(t *testing.T, url string) model.Job {
	t.Helper()

	ctx := context.Background()
	if err := f.queue.EnsureGroup(ctx); err != nil {
		t.Fatalf("creating group: %v", err)
	}
	job := f.submit(t, url)
	d, err := f.queue.Read(ctx, "dead", time.Second)
	if err != nil {
		t.Fatalf("reading job: %v", err)
	}
	if d == nil || d.Job.ID != job.ID {
		t.Fatalf("unexpected delivery: %+v", d)
	}

	return job
}

func (f *fixture) depth(t *testing.T) int64 {
	t.Helper()

	n, err := f.queue.Depth(context.Background())
	if err != nil {
		t.Fatalf("reading depth: %v", err)
	}

	return n
}

// waitForImage waits for the resized image to reach the cache.
func (f *fixture) waitForImage(t *testing.T, job model.Job) {
	t.Helper()

	eventually(t, "the image of job "+job.ID, func() bool {
		return f.cache.Contains(context.Background(), job.ID)
	})
}

// waitFinished waits for the worker to remove the job's state.
func (f *fixture) waitFinished(t *testing.T, job model.Job) {
	t.Helper()

	eventually(t, "job "+job.ID+" to finish", func() bool {
		_, ok, err := f.queue.State(context.Background(), job.ID)
		return err == nil && !ok
	})
}

func (f *fixture) hasState(t *testing.T, job model.Job) bool {
	t.Helper()

	_, ok, err := f.queue.State(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reading state: %v", err)
	}

	return ok
}

func (f *fixture) requireFinished(t *testing.T, job model.Job) {
	t.Helper()

	state, ok, err := f.queue.State(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reading state: %v", err)
	}
	if ok {
		t.Fatalf("job still has state %+v", state)
	}
	if n := f.depth(t); n != 0 {
		t.Fatalf("unexpected depth: %d", n)
	}
}

func (f *fixture) waitFailed(t *testing.T, job model.Job) queue.State {
	t.Helper()

	var state queue.State
	eventually(t, "job "+job.ID+" to fail", func() bool {
		var err error
		state, _, err = f.queue.State(context.Background(), job.ID)
		return err == nil && state.Status == queue.StatusFailed
	})

	return state
}

func (f *fixture) requireResized(t *testing.T, job model.Job) {
	t.Helper()

	data, ok := f.cache.Get(context.Background(), job.ID)
	if !ok {
		t.Fatalf("job %s is not cached", job.ID)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("cached image is not a jpeg: %v", err)
	}
	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
		t.Fatalf("unexpected image size: %v", img.Bounds())
	}
}

// run is a Worker started by a test.
type run struct {
	worker *Worker
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// prepare builds a run without starting it, and stops it when the test ends.
// The source and the client close later, as they were set up first.
func (f *fixture) prepare(t *testing.T) *run {
	t.Helper()

	w := New(f.settings, f.queue, f.cache, f.resizer)
	w.readBlock = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	r := &run{worker: w, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(timeout):
			t.Errorf("Run did not return")
		}
	})

	return r
}

func (f *fixture) start(t *testing.T) *run {
	t.Helper()

	r := f.prepare(t)
	r.launch()

	return r
}

func (r *run) launch() {
	go func() {
		defer close(r.done)
		r.err = r.worker.Run(r.ctx)
	}()
}

func (r *run) requireRunningFor(t *testing.T, d time.Duration) {
	t.Helper()

	select {
	case <-r.done:
		t.Fatal("Run returned early")
	case <-time.After(d):
	}
}

func (r *run) stop(t *testing.T) {
	t.Helper()

	r.cancel()
	r.wait(t)
}

func (r *run) wait(t *testing.T) {
	t.Helper()

	select {
	case <-r.done:
	case <-time.After(timeout):
		t.Fatal("Run did not return")
	}
	if r.err != nil {
		t.Fatalf("Run failed: %v", r.err)
	}
}

// source is an HTTP image source that counts its requests.
type source struct {
	*httptest.Server
	requests atomic.Int64

	mu    sync.Mutex
	order []string
}

func newSource(t *testing.T, respond func(n int64, w http.ResponseWriter, r *http.Request)) *source {
	t.Helper()

	s := &source{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.order = append(s.order, r.URL.Path)
		s.mu.Unlock()
		respond(s.requests.Add(1), w, r)
	}))
	t.Cleanup(s.Close)

	return s
}

// paths lists the requested paths, oldest first.
func (s *source) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.order)
}

func imageSource(t *testing.T) *source {
	t.Helper()

	data := buildJPEG(t, 64, 64)

	return newSource(t, func(_ int64, w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	})
}

// gate holds a source's responses back until it is opened.
type gate struct {
	started  chan struct{}
	opened   chan struct{}
	startOne sync.Once
}

func gatedSource(t *testing.T) (*source, *gate) {
	t.Helper()

	g := &gate{started: make(chan struct{}), opened: make(chan struct{})}
	data := buildJPEG(t, 64, 64)
	src := newSource(t, func(_ int64, w http.ResponseWriter, r *http.Request) {
		g.startOne.Do(func() { close(g.started) })
		select {
		case <-g.opened:
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})

	return src, g
}

func (g *gate) waitStarted(t *testing.T) {
	t.Helper()

	select {
	case <-g.started:
	case <-time.After(timeout):
		t.Fatal("source got no request")
	}
}

func (g *gate) open() {
	close(g.opened)
}

// rejectsFirstAdd fails one cache write, then behaves.
type rejectsFirstAdd struct {
	cache.ImageCacheAdapter
	adds atomic.Int64
}

func (c *rejectsFirstAdd) Add(ctx context.Context, key string, value any) bool {
	if c.adds.Add(1) == 1 {
		return false
	}

	return c.ImageCacheAdapter.Add(ctx, key, value)
}

// hook observes and fails commands of a Redis client.
type hook struct {
	before func(cmd redis.Cmder) error
	after  func(cmd redis.Cmder)
}

func failFirst(command string, times int64) hook {
	var seen atomic.Int64

	return hook{before: func(cmd redis.Cmder) error {
		if cmd.Name() == command && seen.Add(1) <= times {
			return errInjected
		}
		return nil
	}}
}

func (h hook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h hook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.before != nil {
			if err := h.before(cmd); err != nil {
				cmd.SetErr(err)
				return err
			}
		}
		err := next(ctx, cmd)
		if h.after != nil {
			h.after(cmd)
		}

		return err
	}
}

func (h hook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func buildJPEG(t *testing.T, width int, height int) []byte {
	t.Helper()

	data := bytes.Buffer{}
	if err := jpeg.Encode(&data, stdimage.NewRGBA(stdimage.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatalf("encoding jpeg: %v", err)
	}

	return data.Bytes()
}
