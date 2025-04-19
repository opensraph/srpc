package duplex

import "io"

// MessageSender sends a message payload.
type MessageSender interface {
	Send(MessagePayload) (int64, error)
}

// MessagePayload is a sized and seekable message payload. The interface is
// implemented by [*bytes.Reader] and *envelope. Reads must be non-blocking.
type MessagePayload interface {
	io.Reader
	io.WriterTo
	io.Seeker
	Len() int
}
