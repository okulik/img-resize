package image

// Reports whether data is a progressive JPEG, by walking its marker segments up
// to the frame header. Searching the raw bytes won't do, as the bytes of that
// marker can appear inside other segments. Bytes are skipped the way the decoder
// skips them. Data that doesn't lead to a frame header counts as not
// progressive, for the decoder to report.
func isProgressiveJPEG(data []byte) bool {
	const (
		escapedByte      = 0x00
		baselineFrame    = 0xc0
		extendedFrame    = 0xc1
		progressiveFrame = 0xc2
		firstRestart     = 0xd0
		lastRestart      = 0xd7
		endOfImage       = 0xd9
		startOfScan      = 0xda
		startOfImageSize = 2
		lengthSize       = 2
	)

	position := startOfImageSize
	for {
		for position < len(data) && data[position] != 0xff {
			position++
		}
		for position < len(data) && data[position] == 0xff {
			position++
		}
		if position >= len(data) {
			return false
		}

		marker := data[position]
		position++

		switch {
		case marker == progressiveFrame:
			return true
		case marker == baselineFrame, marker == extendedFrame, marker == startOfScan, marker == endOfImage:
			return false
		case marker == escapedByte, marker >= firstRestart && marker <= lastRestart:
			continue
		}

		if position+lengthSize > len(data) {
			return false
		}
		length := int(data[position])<<8 | int(data[position+1])
		if length < lengthSize {
			return false
		}
		position += length
	}
}
