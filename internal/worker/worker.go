// Package worker turns queued jobs into resized images.
package worker

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/settings"
)

const (
	defaultReadBlock = 5 * time.Second
	retryDelay       = time.Second
)

// Worker runs queued resize jobs. A job it can't finish stays pending, so that
// a reclaim runs it again, here or on another worker.
type Worker struct {
	settings  *settings.Settings
	queue     *queue.Queue
	cache     cache.ImageCacheAdapter
	resizer   *image.Resizer
	consumer  string
	readBlock time.Duration
	// slots limits how many jobs run at once, as memory allows no more than
	// WorkerConcurrency resizes. A loop takes a slot before claiming work, so a
	// claimed job always starts immediately, and keeps it until the work is done.
	slots chan struct{}
}

func New(settings *settings.Settings, queue *queue.Queue, imageCache cache.ImageCacheAdapter, resizer *image.Resizer) *Worker {
	return &Worker{
		settings:  settings,
		queue:     queue,
		cache:     imageCache,
		resizer:   resizer,
		consumer:  consumerName(),
		readBlock: defaultReadBlock,
		slots:     make(chan struct{}, max(settings.Service.WorkerConcurrency, 1)),
	}
}

// Run processes jobs until ctx is cancelled, then finishes the jobs it claimed
// and returns. A blocked read can't be interrupted, so that may take as long as
// one read block on top of the jobs themselves. Redis errors never stop it, as
// Redis may simply start later than the worker.
func (w *Worker) Run(ctx context.Context) error {
	if !w.ensureGroup(ctx) {
		return nil
	}

	var wg sync.WaitGroup
	for range cap(w.slots) {
		wg.Go(func() { w.readLoop(ctx) })
	}
	wg.Go(func() { w.reclaimLoop(ctx) })
	wg.Wait()

	return nil
}

// ensureGroup retries until the consumer group exists. It reports false if ctx
// was cancelled first.
func (w *Worker) ensureGroup(ctx context.Context) bool {
	for {
		err := w.queue.EnsureGroup(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}

		log.Printf("preparing the job queue: %v", err)
		if !sleep(ctx, retryDelay) {
			return false
		}
	}
}

func (w *Worker) readLoop(ctx context.Context) {
	for w.acquire(ctx) {
		delivery, err := w.queue.Read(ctx, w.consumer, w.readBlock)
		if delivery != nil {
			// The job counts as delivered now, so it runs even if ctx was
			// cancelled in the meantime.
			w.handle(context.WithoutCancel(ctx), *delivery)
		}
		w.release()

		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}

		log.Printf("reading jobs: %v", err)
		if redis.HasErrorPrefix(err, "NOGROUP") {
			// The stream was evicted or deleted, and the group with it.
			if w.ensureGroup(ctx) {
				continue
			}
			return
		}
		if !sleep(ctx, retryDelay) {
			return
		}
	}
}

func (w *Worker) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(w.settings.Service.JobReclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if !w.acquire(ctx) {
			return
		}
		w.reclaim(context.WithoutCancel(ctx))
		w.release()
	}
}

// reclaim takes ctx uncancelled, as stopping halfway would cost the claimed
// jobs an attempt without running them.
func (w *Worker) reclaim(ctx context.Context) {
	deliveries, _, err := w.queue.Reclaim(ctx, w.consumer)
	if err != nil {
		log.Printf("reclaiming jobs: %v", err)
	}

	for _, delivery := range deliveries {
		w.handle(ctx, delivery)
	}
}

// handle runs one delivery. Returning without Complete or Fail leaves the job
// pending for a reclaim.
func (w *Worker) handle(ctx context.Context, d queue.Delivery) {
	job := d.Job

	if w.cache.Contains(ctx, job.ID) {
		w.complete(ctx, d)
		return
	}

	// The state is only informative, so a job is worth running without it.
	if err := w.queue.MarkProcessing(ctx, job.ID); err != nil {
		log.Printf("job %s: %v", job.ID, err)
	}

	data, err := w.resizer.Resize(ctx, job.URL, job.Width, job.Height)
	if err != nil {
		w.handleFailure(ctx, d, err)
		return
	}

	if !w.cache.Add(ctx, job.ID, data) {
		log.Printf("job %s: attempt %d could not store the image", job.ID, d.Attempts)
		return
	}

	w.complete(ctx, d)
}

// handleFailure fails the job if retrying can't help. Only the reason reaches
// the job's state, as the error holds detail clients must not see.
func (w *Worker) handleFailure(ctx context.Context, d queue.Delivery, err error) {
	var failure *image.Failure
	if !errors.As(err, &failure) || !failure.Permanent {
		log.Printf("job %s: attempt %d failed: %v", d.Job.ID, d.Attempts, err)
		return
	}

	log.Printf("job %s: failed: %v", d.Job.ID, err)
	if err := w.queue.Fail(ctx, d, failure.Reason); err != nil {
		log.Printf("job %s: %v", d.Job.ID, err)
	}
}

func (w *Worker) complete(ctx context.Context, d queue.Delivery) {
	if err := w.queue.Complete(ctx, d); err != nil {
		log.Printf("job %s: %v", d.Job.ID, err)
	}
}

// acquire takes a slot, and reports false if ctx was cancelled first.
func (w *Worker) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	select {
	case w.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *Worker) release() {
	<-w.slots
}

// sleep waits for d, and reports false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// consumerName identifies this worker to the consumer group.
func consumerName() string {
	if id := os.Getenv("FLY_MACHINE_ID"); id != "" {
		return id
	}

	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("reading hostname: %v", err)
		return "worker"
	}

	return hostname
}
