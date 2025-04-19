package errors

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrNotModified signals Connect-protocol responses to GET requests to use the
	// 304 Not Modified HTTP error code.
	ErrNotModified = errors.New("not modified")
	// ErrNotModifiedClient wraps ErrNotModified for use client-side.
	ErrNotModifiedClient = fmt.Errorf("HTTP 304: %w", ErrNotModified)
)

// NewNotModifiedError indicates that the requested resource hasn't changed. It
// should be used only when handlers wish to respond to conditional HTTP GET
// requests with a 304 Not Modified. In all other circumstances, including all
// RPCs using the gRPC or gRPC-Web protocols, it's equivalent to sending an
// error with [CodeUnknown]. The supplied headers should include Etag,
// Cache-Control, or any other headers required by [RFC 9110 § 15.4.5].
//
// Clients should check for this error using [IsNotModifiedError].
//
// [RFC 9110 § 15.4.5]: https://httpwg.org/specs/rfc9110.html#status.304
func NewNotModifiedError(headers http.Header) *Error {
	err := FromError(ErrNotModified).WithCode(Unknown)
	if headers != nil {
		err.meta = headers
	}
	return err
}

// IsNotModifiedError checks whether the supplied error indicates that the
// requested resource hasn't changed. It only returns true if the server used
// [NewNotModifiedError] in response to a Connect-protocol RPC made with an
// HTTP GET.
func IsNotModifiedError(err error) bool {
	return errors.Is(err, ErrNotModified)
}
