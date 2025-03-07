package image

import (
	"context"

	"github.com/okulik/fm-go/internal/model"
)

type ImageResizer interface {
	Start()
	Shutdown()
	Process(request *model.ResizeRequest, ctx context.Context) ([]model.ResizeResponse, error)
	ProcessAsync(request *model.ResizeRequest) []model.ResizeResponse
	ResizingProgress() *ResizingProgress
}
