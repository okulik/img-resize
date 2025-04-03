package model_test

import (
	"regexp"
	"testing"

	"github.com/okulik/img-resize/internal/model"
)

func TestNewResizeRequestFromJSON(t *testing.T) {
	json := `{
		"urls": [
			"https://i.imgur.com/RzW6QSI.jpeg",
			"https://httpstat.us/404"
		],
		"width": 200,
		"height": 0
	}`
	resizeReq, err := model.NewResizeRequestFromJSON([]byte(json))

	if err != nil {
		t.Errorf("Failed to parse JSON: %v", err)
	}

	if len(resizeReq.URLs) != 2 {
		t.Errorf("Unexpected number of URLs: %v", len(resizeReq.URLs))
	}

	if resizeReq.URLs[0] != "https://i.imgur.com/RzW6QSI.jpeg" {
		t.Errorf("Unexpected url value: %v", resizeReq.URLs[0])
	}

	if resizeReq.URLs[1] != "https://httpstat.us/404" {
		t.Errorf("Unexpected url value: %v", resizeReq.URLs[1])
	}

	if resizeReq.Width != 200 {
		t.Errorf("Unexpected width value: %v", resizeReq.Width)
	}

	if resizeReq.Height != 0 {
		t.Errorf("Unexpected height value: %v", resizeReq.Height)
	}
}

func TestNewResizeRequestFromInvalidJSON(t *testing.T) {
	json := `{
		"urls": "https://i.imgur.com/RzW6QSI.jpeg",
		"width": "should-be-a-number",
		"height": 0
	}`
	_, err := model.NewResizeRequestFromJSON([]byte(json))

	if !regexp.MustCompile(`json: cannot unmarshal string into Go struct field ResizeRequest.urls of type \[\]string`).Match([]byte(err.Error())) {
		t.Errorf("Expected to return `cannot unmarshal error...` but got: %v", err)
	}

	json = `{
		"urls": ["https://i.imgur.com/RzW6QSI.jpeg"],
		"width": "should-be-a-number",
		"height": 0
	}`
	_, err = model.NewResizeRequestFromJSON([]byte(json))

	if !regexp.MustCompile(`json: cannot unmarshal string into Go struct field ResizeRequest.width of type uint`).Match([]byte(err.Error())) {
		t.Errorf("Expected to return `cannot unmarshal error...` but got: %v", err)
	}
}
