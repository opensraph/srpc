package utils

import "io"

const discardLimit = 1024 * 1024 * 4 // 4MiB

func Discard(reader io.Reader) (int64, error) {
	if lr, ok := reader.(*io.LimitedReader); ok {
		return io.Copy(io.Discard, lr)
	}
	// We don't want to get stuck throwing data away forever, so limit how much
	// we're willing to do here.
	lr := &io.LimitedReader{R: reader, N: discardLimit}
	return io.Copy(io.Discard, lr)
}
