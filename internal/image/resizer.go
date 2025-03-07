package image

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"sync"

	jpgresize "github.com/nfnt/resize"

	"github.com/okulik/fm-go/internal/cache"
	"github.com/okulik/fm-go/internal/model"
	"github.com/okulik/fm-go/internal/settings"
)

const (
	maxResizeJobsSize   = 10000
	maxResizeJobWorkers = 4

	statusSuccess  = "success"
	statusFailure  = "failure"
	statusEnqueued = "enqueued"
)

// ResizeJob represents a single image resize task.
type ResizeJob struct {
	URL    string
	Width  uint
	Height uint
}

// Resizer represents an image resizing engine. It supports both
// synchronous and asynchronous image resizing.
type Resizer struct {
	settings         *settings.Settings
	imageCache       cache.ImageCacheAdapter
	resizeJobs       chan *ResizeJob
	resizingProgress *ResizingProgress
	wg               sync.WaitGroup
}

// Creates a new instance of the Resizer object.
func NewResizer(settings *settings.Settings, imageCache cache.ImageCacheAdapter) *Resizer {
	return &Resizer{
		settings:         settings,
		imageCache:       imageCache,
		resizeJobs:       make(chan *ResizeJob, maxResizeJobsSize),
		resizingProgress: NewResizingProgress(settings),
	}
}

// Starts a pool of background workers for async image resizing.
func (r *Resizer) Start() {
	if !r.settings.Service.AsyncResize {
		return
	}

	log.Print("async resizing enabled")

	for i := 0; i < maxResizeJobWorkers; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()

			for job := range r.resizeJobs {
				_, _ = r.processImageResize(context.Background(), job.URL, job.Width, job.Height)
				r.resizingProgress.DeleteResizing(genImageID(job.URL, job.Width, job.Height))
			}
		}()
	}
}

// Stops all background workers.
func (r *Resizer) Shutdown() {
	if !r.settings.Service.AsyncResize {
		return
	}

	close(r.resizeJobs)
	r.wg.Wait()
}

// Resize a batch of images, identified by their URLs, asynchronously. This
// method processes the images provided in the request by enqueueing each of
// them into the image resizing channel. Once all requested images are enqueued,
// the method returns immediately with basic information, such as image IDs.
// These IDs can be used in subsequent calls to retrieve the resized images from
// the cache.
func (r *Resizer) ProcessAsync(request *model.ResizeRequest) []model.ResizeResponse {
	results := make([]model.ResizeResponse, 0, len(request.URLs))

	for _, url := range request.URLs {
		imageID := genImageID(url, request.Width, request.Height)

		// Check if the image is cached
		if r.imageCache.Contains(imageID) {
			results = append(results, model.ResizeResponse{ID: imageID, Result: statusSuccess, Cached: true})
			continue
		}

		// Check if the image is already being resized; if not, mark it as being resized
		if r.resizingProgress.CheckAndSetResizing(imageID) {
			results = append(results, model.ResizeResponse{ID: imageID, Result: statusEnqueued, Cached: false})
			continue
		}

		if ok := r.trySendResizeJob(url, request.Width, request.Height); !ok {
			log.Print("image resize queue full, try later")
			results = append(results, model.ResizeResponse{Result: statusFailure})
			r.resizingProgress.DeleteResizing(imageID)
			continue
		}

		results = append(results, model.ResizeResponse{ID: imageID, Result: statusEnqueued, Cached: false})
	}

	return results
}

// Synchronously resize a batch of images, identified by their URLs.
func (r *Resizer) Process(request *model.ResizeRequest, ctx context.Context) ([]model.ResizeResponse, error) {
	results := make([]model.ResizeResponse, 0, len(request.URLs))

	for _, url := range request.URLs {
		resp, err := r.processImageResize(ctx, url, request.Width, request.Height)
		if err != nil {
			results = append(results, resp)
		}
	}

	return results, nil
}

func (r *Resizer) ResizingProgress() *ResizingProgress {
	return r.resizingProgress
}

func (r *Resizer) processImageResize(ctx context.Context, url string, width uint, height uint) (model.ResizeResponse, error) {
	imageID := genImageID(url, width, height)

	// First check if the image is already cached
	if r.imageCache.Contains(imageID) {
		return model.ResizeResponse{ID: imageID, Result: statusSuccess, Cached: true}, nil
	}

	// Retrieve the image from the url
	data, err := r.fetchAndResize(ctx, url, width, height)
	if err != nil {
		log.Printf("failed to resize %s: %v", url, err)
		return model.ResizeResponse{Result: statusFailure}, err
	}

	log.Print("caching ", imageID)
	r.imageCache.Add(imageID, data)

	return model.ResizeResponse{ID: imageID, Result: statusSuccess, Cached: false}, nil
}

func (r *Resizer) fetchAndResize(ctx context.Context, url string, width uint, height uint) ([]byte, error) {
	data, err := r.fetch(ctx, url)
	if err != nil {
		return nil, err
	}

	return r.resize(data, width, height)
}

func (r *Resizer) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.settings.Http.ClientUserAgent)
	log.Print("fetching ", url)
	res, err := new(http.Client).Do(req)
	if err != nil {
		return nil, fmt.Errorf("image fetch failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 status: %d", res.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(res.Body, r.settings.Service.MaxImageSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %v", err)
	}

	return data, nil
}

func (r *Resizer) resize(data []byte, width uint, height uint) ([]byte, error) {
	// decode jpeg into image.Image
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode jpeg: %v", err)
	}

	// if either width or height is 0, it will resize respecting the aspect ratio
	newImage := jpgresize.Resize(width, height, img, jpgresize.Lanczos3)

	newData := bytes.Buffer{}
	err = jpeg.Encode(bufio.NewWriter(&newData), newImage, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to jpeg encode resized image: %v", err)
	}

	return newData.Bytes(), nil
}

func (r *Resizer) trySendResizeJob(url string, width uint, height uint) bool {
	// Enqueue async resize job
	job := &ResizeJob{URL: url, Width: width, Height: height}

	select {
	case r.resizeJobs <- job:
		return true
	default:
		return false
	}
}

func genImageID(url string, width uint, height uint) string {
	sha := sha256.Sum256([]byte(fmt.Sprintf("%s,%d,%d", url, width, height)))
	return hex.EncodeToString(sha[:])
}
