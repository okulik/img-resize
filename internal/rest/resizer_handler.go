package rest

import (
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"

	chi "github.com/go-chi/chi/v5"
	"github.com/pkg/errors"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/metrics"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/ratelimit"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/web"
)

const (
	maxRequestSize     = 8 * 1024
	maxBatchImageCount = 100
	retryAfterSeconds  = "5"
	imageCacheControl  = "private, max-age=900"

	// The buckets are named like the limit label of the throttled metric.
	requestsBucket = metrics.LimitRequests
	bytesBucket    = metrics.LimitBytes

	statusSuccess  = "success"
	statusFailure  = "failure"
	statusEnqueued = "enqueued"
)

type ResizerHandler struct {
	settings   *settings.Settings
	imageCache cache.ImageCacheAdapter
	queue      *queue.Queue
	limiter    *ratelimit.Limiter
	metrics    *metrics.Metrics
}

// Creates a new instance of ResizerHandler object.
func NewResizerHandler(settings *settings.Settings, imageCache cache.ImageCacheAdapter, queue *queue.Queue, limiter *ratelimit.Limiter, metrics *metrics.Metrics) *ResizerHandler {
	return &ResizerHandler{
		settings:   settings,
		imageCache: imageCache,
		queue:      queue,
		limiter:    limiter,
		metrics:    metrics,
	}
}

// A web handler for resizing images. Each image is enqueued as a job for a fleet
// of background workers, and the resized images are stored to the image cache.
// The response lists a result with the image ID for every URL, which a client
// uses to retrieve the image once it's resized. With sync=true the request takes
// one URL and waits for the resized image instead.
func (rh *ResizerHandler) ResizeImage(w http.ResponseWriter, r *http.Request) {
	// Limit POST body size to up to maxRequestSize bytes
	buffer, err := io.ReadAll(io.LimitReader(r.Body, maxRequestSize))
	if err != nil {
		web.WriteErrorResponse(w, errors.Wrap(err, "failed to read request body"), http.StatusBadRequest)
		return
	}

	resizeReq, err := model.NewResizeRequestFromJSON(buffer)
	if err != nil {
		web.WriteErrorResponse(w, errors.Wrap(err, "invalid resize request body"), http.StatusBadRequest)
		return
	}

	// Limit the number of images we can resize with a single call
	if len(resizeReq.URLs) > maxBatchImageCount {
		web.WriteErrorResponse(w, errors.Errorf("number of images in a batch is limited to %d", maxBatchImageCount), http.StatusBadRequest)
		return
	}

	syncMode := r.URL.Query().Get("sync") == "true"
	if syncMode && len(resizeReq.URLs) != 1 {
		web.WriteErrorResponse(w, errors.New("sync mode takes exactly one url"), http.StatusBadRequest)
		return
	}

	// Limit the requested dimensions, as memory use grows with the output size
	maxDimension := rh.settings.Service.MaxImageDimension
	if resizeReq.Width > maxDimension || resizeReq.Height > maxDimension {
		web.WriteErrorResponse(w, errors.Errorf("image width and height are limited to %d", maxDimension), http.StatusBadRequest)
		return
	}

	if !rh.allowRequest(w, r, len(resizeReq.URLs)) {
		return
	}

	// Refuse the request while the queue is full. The depth is read once, so a
	// batch may overshoot the limit by up to maxBatchImageCount jobs.
	depth, err := rh.queue.Depth(r.Context())
	if err != nil {
		rh.writeUnavailable(w, err)
		return
	}
	if depth >= rh.settings.Service.MaxQueueDepth {
		log.Printf("job queue full at %d jobs, rejecting request", depth)
		rh.metrics.Rejected.WithLabelValues(metrics.ReasonQueueFull).Inc()
		rh.writeUnavailable(w, nil)
		return
	}

	if syncMode {
		rh.resizeAndWait(w, r, resizeReq.URLs[0], resizeReq.Width, resizeReq.Height)
		return
	}

	results := make([]model.ResizeResponse, 0, len(resizeReq.URLs))
	for _, url := range resizeReq.URLs {
		result, err := rh.submit(r.Context(), url, resizeReq.Width, resizeReq.Height)
		if err != nil {
			rh.writeUnavailable(w, err)
			return
		}
		results = append(results, result)
	}

	web.WriteJSONResponse(w, results, http.StatusOK)
}

// Counts the URLs of a request against the limit of the client's network and
// answers 429 or 503 when the request must not go on.
func (rh *ResizerHandler) allowRequest(w http.ResponseWriter, r *http.Request, urls int) bool {
	return rh.allow(w, r, requestsBucket, int64(urls), rh.settings.Service.RateLimitPerMinute)
}

// Counts amount in bucket against limit for the client's network, and answers
// 429 or 503 when the request must not go on. Clients are told apart by prefix,
// as one IPv6 client usually controls many addresses.
func (rh *ResizerHandler) allow(w http.ResponseWriter, r *http.Request, bucket string, amount int64, limit int64) bool {
	cfg := rh.settings.Service
	key := ratelimit.PrefixKey(ratelimit.ClientIP(r, cfg.TrustFlyClientIP), cfg.RateLimitIPv4Prefix, cfg.RateLimitIPv6Prefix)

	allowed, retryAfter, err := rh.limiter.Allow(r.Context(), bucket, key, amount, limit)
	if err != nil {
		rh.writeUnavailable(w, err)
		return false
	}
	if !allowed {
		rh.metrics.Throttled.WithLabelValues(bucket).Inc()
		seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		web.WriteErrorResponse(w, errors.New("too many requests"), http.StatusTooManyRequests)
		return false
	}

	return true
}

// Resizes one image and answers with it, waiting up to SyncWaitTimeout for a
// worker to finish. A job still unfinished then is answered like an image
// fetched while its job runs, so the client can fetch it later. If the client
// disconnects there is nobody to answer, and the job keeps running.
func (rh *ResizerHandler) resizeAndWait(w http.ResponseWriter, r *http.Request, url string, width uint, height uint) {
	ctx := r.Context()
	id := model.ImageID(url, width, height)

	data, ok, err := rh.imageCache.Get(ctx, id)
	if err != nil {
		rh.writeUnavailable(w, err)
		return
	}
	if ok {
		rh.writeImageResponse(w, r, data)
		return
	}

	submission, err := rh.queue.Submit(ctx, model.Job{ID: id, URL: url, Width: width, Height: height})
	if err != nil {
		rh.writeUnavailable(w, err)
		return
	}
	if submission == queue.Failed {
		// The job failed already, so report that instead of waiting.
		rh.serveImage(w, r, id)
		return
	}

	res, err := rh.queue.Wait(ctx, id, rh.settings.Service.SyncWaitTimeout)
	if err != nil {
		if ctx.Err() != nil {
			// A client that left won't read this, but when the server shuts down
			// first, the client learns the ID to fetch the image with later.
			writeJobPending(w, id)
			return
		}
		rh.writeUnavailable(w, err)
		return
	}

	switch res.Outcome {
	case queue.WaitFailed:
		writeJobFailed(w, id, res.Error)
	case queue.WaitDone:
		data, ok, err := rh.imageCache.Get(ctx, id)
		if err != nil {
			rh.writeUnavailable(w, err)
			return
		}
		if ok {
			rh.writeImageResponse(w, r, data)
			return
		}
		// The image is gone already, so let the client ask again.
		writeJobPending(w, id)
	default:
		writeJobPending(w, id)
	}
}

// Reports the outcome of a resize of one image, enqueueing a job unless the
// image is cached or its job already exists.
func (rh *ResizerHandler) submit(ctx context.Context, url string, width uint, height uint) (model.ResizeResponse, error) {
	id := model.ImageID(url, width, height)
	cached, err := rh.imageCache.Contains(ctx, id)
	if err != nil {
		return model.ResizeResponse{}, err
	}
	if cached {
		return model.ResizeResponse{Result: statusSuccess, ID: id, Cached: true}, nil
	}

	submission, err := rh.queue.Submit(ctx, model.Job{ID: id, URL: url, Width: width, Height: height})
	if err != nil {
		return model.ResizeResponse{}, err
	}
	if submission == queue.Failed {
		return model.ResizeResponse{Result: statusFailure, ID: id}, nil
	}

	return model.ResizeResponse{Result: statusEnqueued, ID: id}, nil
}

// Answers 503 with generic text, as an error from Redis holds detail clients
// must not see. A nil err means the queue is full.
func (rh *ResizerHandler) writeUnavailable(w http.ResponseWriter, err error) {
	if err != nil {
		log.Printf("resize request failed: %v", err)
	}
	w.Header().Set("Retry-After", retryAfterSeconds)
	web.WriteErrorResponse(w, errors.New("service unavailable"), http.StatusServiceUnavailable)
}

// A web handler for retrieving resized images from the image cache. While there
// is no image it reports the state of the job that makes it: 202 while the job
// is queued or running, 422 once it failed, and 404 if there is no such job. A
// Redis failure is 503, as it says nothing about the image.
func (rh *ResizerHandler) GetImage(w http.ResponseWriter, r *http.Request) {
	rh.serveImage(w, r, chi.URLParam(r, "imageID"))
}

// Answers with the image if there is one, or else with the state of its job.
func (rh *ResizerHandler) serveImage(w http.ResponseWriter, r *http.Request, imageID string) {
	ctx := r.Context()
	data, ok, err := rh.imageCache.Get(ctx, imageID)
	if err != nil {
		rh.writeUnavailable(w, err)
		return
	}
	if ok {
		rh.writeImageResponse(w, r, data)
		return
	}

	state, found, err := rh.queue.State(ctx, imageID)
	if err != nil {
		rh.writeUnavailable(w, err)
		return
	}
	if !found {
		// The job may have finished since the first read, storing the image and
		// then dropping its state, so look once more before calling it unknown.
		data, ok, err := rh.imageCache.Get(ctx, imageID)
		if err != nil {
			rh.writeUnavailable(w, err)
			return
		}
		if ok {
			rh.writeImageResponse(w, r, data)
			return
		}

		web.WriteErrorResponse(w, errors.New("image not cached"), http.StatusNotFound)
		return
	}

	if state.Status == queue.StatusFailed {
		writeJobFailed(w, imageID, state.Error)
		return
	}

	writeJobPending(w, imageID)
}

func writeJobFailed(w http.ResponseWriter, id string, reason string) {
	web.WriteJSONResponse(w, model.ResizeResponse{Result: statusFailure, ID: id, Error: reason}, http.StatusUnprocessableEntity)
}

func writeJobPending(w http.ResponseWriter, id string) {
	web.WriteJSONResponse(w, model.ResizeResponse{Result: statusEnqueued, ID: id}, http.StatusAccepted)
}

// Sends an image after counting its size against the byte limit of the client's
// network. Every image leaves through here, and nothing is written before the
// count is allowed.
func (rh *ResizerHandler) writeImageResponse(w http.ResponseWriter, r *http.Request, data []byte) {
	if !rh.allow(w, r, bytesBucket, int64(len(data)), rh.settings.Service.RateLimitBytesPerMinute) {
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", imageCacheControl)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		// The status is sent, so all that is left is to log.
		log.Printf("writing image response: %v", err)
		return
	}
	rh.metrics.Bandwidth.WithLabelValues(metrics.DirectionServed).Add(float64(len(data)))
}
