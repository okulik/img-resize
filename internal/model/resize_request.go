package model

import (
	"encoding/json"
)

type ResizeRequest struct {
	URLs   []string `json:"urls"`
	Width  uint     `json:"width"`
	Height uint     `json:"height"`
}

func NewResizeRequestFromJSON(data []byte) (*ResizeRequest, error) {
	var req ResizeRequest
	err := json.Unmarshal(data, &req)
	return &req, err
}
