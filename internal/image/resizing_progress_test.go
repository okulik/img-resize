package image_test

import (
	"testing"
	"time"

	"github.com/okulik/img-resize/internal/image"
	"github.com/okulik/img-resize/internal/settings"
)

func TestWaitForResizingDoneReturnsWhenResizeFinishes(t *testing.T) {
	progress := buildProgress(2 * time.Second)
	progress.CheckAndSetResizing("foo")

	result := make(chan bool, 1)
	go func() { result <- progress.WaitForResizingDone("foo") }()
	time.Sleep(50 * time.Millisecond)

	// A new image arriving while another one is being waited for must not
	// keep the finished resize from being reported.
	go progress.CheckAndSetResizing("bar")
	time.Sleep(50 * time.Millisecond)

	go progress.DeleteResizing("foo")

	select {
	case ok := <-result:
		if !ok {
			t.Error("wait reported a timeout for a finished resize")
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after the resize finished")
	}
}

func TestWaitForResizingDoneTimesOut(t *testing.T) {
	progress := buildProgress(50 * time.Millisecond)
	progress.CheckAndSetResizing("foo")

	if progress.WaitForResizingDone("foo") {
		t.Error("wait reported success for an unfinished resize")
	}
}

func buildProgress(timeout time.Duration) *image.ResizingProgress {
	return image.NewResizingProgress(&settings.Settings{
		Service: &settings.ServiceSettings{ImageResizeTimeout: timeout},
	})
}
