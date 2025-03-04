package model

type ResizeResponse struct {
	Result string `json:"result"`
	ID     string `json:"id,omitempty"`
	Cached bool   `json:"cached"`
}
