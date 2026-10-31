package model

type ResizeResponse struct {
	Result string `json:"result"`
	ID     string `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
	Cached bool   `json:"cached"`
}
