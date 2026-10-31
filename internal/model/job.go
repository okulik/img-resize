package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Job is a unit of resize work. ID is the ImageID of its URL and size.
type Job struct {
	ID     string
	URL    string
	Width  uint
	Height uint
}

func ImageID(url string, width uint, height uint) string {
	sha := sha256.Sum256([]byte(fmt.Sprintf("%s,%d,%d", url, width, height)))
	return hex.EncodeToString(sha[:])
}
