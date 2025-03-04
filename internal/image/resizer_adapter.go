package image

import (
	"github.com/okulik/fm-go/internal/model"
)

type ImageResizer interface {
	Start()
	Shutdown()
	Process(request *model.ResizeRequest) ([]model.ResizeResponse, error)
	ProcessAsync(request *model.ResizeRequest) []model.ResizeResponse
	ResizingProgress() *ResizingProgress
}
