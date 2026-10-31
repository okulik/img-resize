package image

import (
	"os"
	"testing"
)

func TestIsProgressiveJPEG(t *testing.T) {
	const (
		frame     = "\xff\xc0\x00\x0b\x08\x00\x01\x00\x01\x01\x01\x11\x00"
		extended  = "\xff\xc1\x00\x0b\x08\x00\x01\x00\x01\x01\x01\x11\x00"
		prog      = "\xff\xc2\x00\x0b\x08\x00\x01\x00\x01\x01\x01\x11\x00"
		comment   = "\xff\xfe\x00\x04ab"
		scan      = "\xff\xda\x00\x08\x01\x01\x00\x00\x3f\x00"
		endOfFile = "\xff\xd9"
	)

	tests := []struct {
		name string
		data string
		want bool
	}{
		{"baseline", "\xff\xd8" + frame + scan + endOfFile, false},
		{"extended sequential", "\xff\xd8" + extended + scan + endOfFile, false},
		{"progressive", "\xff\xd8" + prog + scan + endOfFile, true},
		{"progressive after a segment", "\xff\xd8" + comment + prog, true},
		{"progressive after fill bytes", "\xff\xd8\xff\xff\xff" + prog[1:], true},
		{"progressive after extraneous bytes", "\xff\xd8abc" + prog, true},
		{"progressive after an escaped byte", "\xff\xd8\xff\x00" + prog, true},
		{"progressive after a restart marker", "\xff\xd8\xff\xd0" + prog, true},
		{"progressive marker inside a comment", "\xff\xd8\xff\xfe\x00\x04\xff\xc2" + frame, false},
		{"progressive marker inside a scan", "\xff\xd8" + scan + prog, false},
		{"progressive marker after the end of the image", "\xff\xd8" + endOfFile + prog, false},
		{"empty", "", false},
		{"start of image only", "\xff\xd8", false},
		{"lone marker prefix", "\xff\xd8\xff", false},
		{"marker without a length", "\xff\xd8\xff\xfe", false},
		{"marker with half a length", "\xff\xd8\xff\xfe\x00", false},
		{"length below its own size", "\xff\xd8\xff\xfe\x00\x01" + prog, false},
		{"length past the end", "\xff\xd8\xff\xfe\xff\xff" + prog, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isProgressiveJPEG([]byte(test.data)); got != test.want {
				t.Errorf("unexpected result: got %v want %v", got, test.want)
			}
		})
	}
}

func TestIsProgressiveJPEGSurvivesEveryTruncation(t *testing.T) {
	data, err := os.ReadFile("testdata/progressive.jpg")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	if !isProgressiveJPEG(data) {
		t.Error("expected the fixture to be progressive")
	}

	for size := range len(data) {
		isProgressiveJPEG(data[:size])
	}
}
