package image

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"

	jpgresize "github.com/nfnt/resize"

	"github.com/okulik/img-resize/internal/settings"
)

// Resizer fetches images and resizes them.
type Resizer struct {
	settings   *settings.Settings
	httpClient *http.Client
}

// Creates a new instance of the Resizer object.
func NewResizer(settings *settings.Settings) *Resizer {
	return &Resizer{
		settings:   settings,
		httpClient: newHTTPClient(settings),
	}
}

// Fetches an image and resizes it, without touching the cache. Every error is
// a *Failure.
func (r *Resizer) Resize(ctx context.Context, url string, width uint, height uint) ([]byte, error) {
	data, err := r.fetch(ctx, url)
	if err != nil {
		return nil, err
	}

	return r.resize(data, width, height)
}

func (r *Resizer) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, permanent(ReasonFetch, err)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, permanent(ReasonFetch, fmt.Errorf("%w: %q", errUnsupportedScheme, req.URL.Scheme))
	}
	req.Header.Set("User-Agent", r.settings.Http.ClientUserAgent)
	log.Print("fetching ", url)
	res, err := r.httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("image fetch failed: %w", err)
		if errors.Is(err, errInternalAddress) {
			return nil, permanent(ReasonFetch, err)
		}

		return nil, transient(ReasonFetch, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		err = fmt.Errorf("non-200 status: %d", res.StatusCode)
		if isTransientStatus(res.StatusCode) {
			return nil, transient(ReasonFetch, err)
		}

		return nil, permanent(ReasonFetch, err)
	}

	// read one byte more than allowed, so that an oversized body is detected
	// instead of silently truncated
	maxSize := r.settings.Service.MaxImageSize
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSize+1))
	if err != nil {
		return nil, transient(ReasonFetch, fmt.Errorf("failed to read image data: %w", err))
	}

	if int64(len(data)) > maxSize {
		return nil, permanent(ReasonTooLarge, fmt.Errorf("image exceeds the size limit of %d bytes", maxSize))
	}

	return data, nil
}

// Reports whether a source answering with the status may answer differently
// later.
func isTransientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || status >= 500
}

func (r *Resizer) resize(data []byte, width uint, height uint) ([]byte, error) {
	// read the image size from its header, as decoding allocates memory for all
	// of the pixels and a small file can declare a very large image
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, permanent(ReasonInvalid, fmt.Errorf("failed to decode jpeg config: %w", err))
	}

	if !isSupportedColorModel(config.ColorModel) {
		return nil, permanent(ReasonInvalid, errors.New("jpeg color model is neither YCbCr nor grayscale"))
	}

	pixels := int64(config.Width) * int64(config.Height)
	if pixels > r.settings.Service.MaxImagePixels {
		return nil, permanent(ReasonTooLarge, fmt.Errorf("image size %dx%d exceeds the pixel limit", config.Width, config.Height))
	}

	// the decoder keeps every coefficient of a progressive image in memory, at
	// 4 bytes each, on top of the pixels
	if pixels > r.settings.Service.MaxImageProgressivePixels && isProgressiveJPEG(data) {
		return nil, permanent(ReasonTooLarge, fmt.Errorf("progressive image size %dx%d exceeds the pixel limit", config.Width, config.Height))
	}

	if config.Width == 0 || config.Height == 0 {
		return nil, permanent(ReasonInvalid, fmt.Errorf("image size %dx%d is empty", config.Width, config.Height))
	}

	maxSourceDimension := int(r.settings.Service.MaxImageSourceDimension)
	if config.Width > maxSourceDimension || config.Height > maxSourceDimension {
		return nil, permanent(ReasonTooLarge, fmt.Errorf("image size %dx%d exceeds the source dimension limit", config.Width, config.Height))
	}

	maxDimension := r.settings.Service.MaxImageDimension
	outWidth, outHeight := outputSize(uint(config.Width), uint(config.Height), width, height)
	if outWidth > maxDimension || outHeight > maxDimension {
		return nil, permanent(ReasonTooLarge, fmt.Errorf("resized image size %dx%d exceeds the dimension limit", outWidth, outHeight))
	}

	// decode jpeg into image.Image
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, permanent(ReasonInvalid, fmt.Errorf("failed to decode jpeg: %w", err))
	}

	// DecodeConfig stops at the first scan, while a later Adobe marker can still
	// turn the decoded image into RGB
	if !isSupportedColorModel(img.ColorModel()) {
		return nil, permanent(ReasonInvalid, errors.New("decoded jpeg color model is neither YCbCr nor grayscale"))
	}

	// if either width or height is 0, it will resize respecting the aspect ratio
	newImage := jpgresize.Resize(width, height, img, jpgresize.Lanczos3)

	newData := bytes.Buffer{}
	err = jpeg.Encode(bufio.NewWriter(&newData), newImage, nil)
	if err != nil {
		return nil, permanent(ReasonInvalid, fmt.Errorf("failed to jpeg encode resized image: %w", err))
	}

	return newData.Bytes(), nil
}

// Reports whether images of a color model stay within the memory budget. The
// resize library's temporary buffers are smallest for YCbCr and grayscale.
func isSupportedColorModel(model color.Model) bool {
	return model == color.YCbCrModel || model == color.GrayModel
}

// Calculates the size of a resized image. If either width or height is 0, it is
// derived from the source image, respecting the aspect ratio.
func outputSize(sourceWidth uint, sourceHeight uint, width uint, height uint) (uint, uint) {
	switch {
	case width == 0 && height == 0:
		return sourceWidth, sourceHeight
	case width == 0:
		return sourceWidth * height / sourceHeight, height
	case height == 0:
		return width, sourceHeight * width / sourceWidth
	}

	return width, height
}
