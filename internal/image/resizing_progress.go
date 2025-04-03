package image

import (
	"log"
	"sync"
	"time"

	"github.com/okulik/img-resize/internal/settings"
)

type ResizingProgress struct {
	settings *settings.Settings
	resizing map[string]chan struct{}
	mu       sync.RWMutex
}

func NewResizingProgress(settings *settings.Settings) *ResizingProgress {
	return &ResizingProgress{
		settings: settings,
		resizing: make(map[string]chan struct{}),
	}
}

// Atomically checks if an image is being resized and marks it as in progress.
func (rp *ResizingProgress) CheckAndSetResizing(imageID string) bool {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	if _, ok := rp.resizing[imageID]; ok {
		return true
	}

	rp.resizing[imageID] = make(chan struct{})

	return false
}

// Checks if an image is being resized.
func (rp *ResizingProgress) CheckResizing(imageID string) bool {
	rp.mu.RLock()
	defer rp.mu.RUnlock()

	_, ok := rp.resizing[imageID]
	return ok
}

// Marks the image as being resized.
func (rp *ResizingProgress) SetResizing(imageID string) {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	rp.resizing[imageID] = make(chan struct{})
}

// Unmarks the image as being resized.
func (rp *ResizingProgress) DeleteResizing(imageID string) {
	rp.mu.RLock()
	ch, ok := rp.resizing[imageID]
	if !ok {
		rp.mu.RUnlock()
		return
	}
	close(ch)
	rp.mu.RUnlock()

	rp.mu.Lock()
	delete(rp.resizing, imageID)
	rp.mu.Unlock()
}

// Synchronously blocks until the image has been resized or the timer expires,
// whichever happens first.
func (rp *ResizingProgress) WaitForResizingDone(imageID string) bool {
	rp.mu.RLock()
	defer rp.mu.RUnlock()

	ch, ok := rp.resizing[imageID]
	if !ok {
		return true
	}

	log.Printf("waiting for resize of %s to finish", imageID)

	select {
	case <-ch:
	case <-time.After(rp.settings.Service.ImageResizeTimeout):
		return false
	}

	return true
}
