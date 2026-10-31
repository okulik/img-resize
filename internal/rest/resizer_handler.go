package rest

import (
	"context"
	"io"
	"log"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/pkg/errors"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/queue"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/web"
)

const (
	maxRequestSize     = 8 * 1024
	maxBatchImageCount = 100
	retryAfterSeconds  = "5"

	statusSuccess  = "success"
	statusFailure  = "failure"
	statusEnqueued = "enqueued"
)

type ResizerHandler struct {
	settings   *settings.Settings
	imageCache cache.ImageCacheAdapter
	queue      *queue.Queue
}

// Creates a new instance of ResizerHandler object.
func NewResizerHandler(settings *settings.Settings, imageCache cache.ImageCacheAdapter, queue *queue.Queue) *ResizerHandler {
	return &ResizerHandler{
		settings:   settings,
		imageCache: imageCache,
		queue:      queue,
	}
}

// A web handler for resizing images. Each image is enqueued as a job for a fleet
// of background workers, and the resized images are stored to the image cache.
// The response lists a result with the image ID for every URL, which a client
// uses to retrieve the image once it's resized.
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

	// Limit the requested dimensions, as memory use grows with the output size
	maxDimension := rh.settings.Service.MaxImageDimension
	if resizeReq.Width > maxDimension || resizeReq.Height > maxDimension {
		web.WriteErrorResponse(w, errors.Errorf("image width and height are limited to %d", maxDimension), http.StatusBadRequest)
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
		rh.writeUnavailable(w, nil)
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

// Reports the outcome of a resize of one image, enqueueing a job unless the
// image is cached or its job already exists.
func (rh *ResizerHandler) submit(ctx context.Context, url string, width uint, height uint) (model.ResizeResponse, error) {
	id := model.ImageID(url, width, height)
	if rh.imageCache.Contains(ctx, id) {
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

// A web handler for retrieving resized images from the image cache.
func (rh *ResizerHandler) GetImage(w http.ResponseWriter, r *http.Request) {
	imageID := chi.URLParam(r, "imageID")

	data, ok := rh.imageCache.Get(r.Context(), imageID)
	if ok {
		writeImageResponse(w, data)
		return
	}

	web.WriteErrorResponse(w, errors.New("image not cached"), http.StatusNotFound)
}

func writeImageResponse(w http.ResponseWriter, data []byte) {
	w.WriteHeader(http.StatusOK)
	w.Header().Add("content-type", "image/jpeg")
	_, err := w.Write(data)
	if err != nil {
		web.WriteErrorResponse(w, errors.New("unable to write a response"), http.StatusInternalServerError)
	}
}
