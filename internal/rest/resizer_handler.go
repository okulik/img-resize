package rest

import (
	"io"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/pkg/errors"

	"github.com/okulik/img-resize/internal/cache"
	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/settings"
	"github.com/okulik/img-resize/internal/web"
)

const (
	maxRequestSize     = 8 * 1024
	maxBatchImageCount = 100
)

type ResizerHandler struct {
	settings   *settings.Settings
	imageCache cache.ImageCacheAdapter
	resizer    image.ImageResizer
}

// Creates a new instance of ResizerHandler object.
func NewResizerHandler(settings *settings.Settings, imageCache cache.ImageCacheAdapter, resizer image.ImageResizer) *ResizerHandler {
	return &ResizerHandler{
		settings:   settings,
		imageCache: imageCache,
		resizer:    resizer,
	}
}

// A web handler for resizing images. Depending on the "async" query parameter, images
// are resized either synchronously (the image is resized before the handler returns
// to the caller) or asynchronously (the resizing job is enqueued for processing by a
// fleet of background workers). The resized images are stored to an in-memory cache.
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

	if isAsyncResize(r) {
		if !rh.settings.Service.AsyncResize {
			web.WriteErrorResponse(w, errors.New("async resize is disabled"), http.StatusFailedDependency)
			return
		}
		resp := rh.resizer.ProcessAsync(resizeReq)
		web.WriteJSONResponse(w, resp, http.StatusOK)
		return
	}

	resp, err := rh.resizer.Process(resizeReq, r.Context())
	if err != nil {
		web.WriteErrorResponse(w, errors.Wrap(err, "failed to resize images"), http.StatusInternalServerError)
		return
	}
	web.WriteJSONResponse(w, resp, http.StatusCreated)
}

// A web handler for retrieving resized images from the in-memory cache.
func (rh *ResizerHandler) GetImage(w http.ResponseWriter, r *http.Request) {
	imageID := chi.URLParam(r, "imageID")

	// If image is being resized, perform a blocking call (with a timeout)
	if !rh.resizer.ResizingProgress().WaitForResizingDone(imageID) {
		web.WriteErrorResponse(w, errors.New("image resize timeout"), http.StatusNotFound)
		return
	}

	// Check if the image was cached
	data, ok := rh.imageCache.Get(r.Context(), imageID)
	if ok {
		writeImageResponse(w, data)
		return
	}

	web.WriteErrorResponse(w, errors.New("image not cached"), http.StatusNotFound)
}

func isAsyncResize(r *http.Request) bool {
	async := r.URL.Query().Get("async")
	return async == "true" || async == "1"
}

func writeImageResponse(w http.ResponseWriter, data []byte) {
	w.WriteHeader(http.StatusOK)
	w.Header().Add("content-type", "image/jpeg")
	_, err := w.Write(data)
	if err != nil {
		web.WriteErrorResponse(w, errors.New("unable to write a response"), http.StatusInternalServerError)
	}
}
