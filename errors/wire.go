package errors

import "net/http"

// NewWireError is similar to [NewError], but the resulting *Error returns true
// when tested with IsWireError.
//
// This is useful for clients trying to propagate partial failures from
// streaming RPCs. Often, these RPCs include error information in their
// response messages (for example, [gRPC server reflection] and
// OpenTelemetry's [OTLP]). Clients propagating these errors up the stack
// should use NewWireError to clarify that the error code, message, and details
// (if any) were explicitly sent by the server rather than inferred from a
// lower-level networking error or timeout.
//
// [gRPC server reflection]: https://github.com/grpc/grpc/blob/v1.49.2/src/proto/grpc/reflection/v1alpha/reflection.proto#L132-L136
// [OTLP]: https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/protocol/otlp.md#partial-success
func NewWireError(c Code, underlying error) *Error {
	err := FromError(underlying).WithCode(c)
	err.wireErr = true
	return err
}

func (e *Error) IsWireError() bool {
	return e.wireErr
}

func (e *Error) Meta() http.Header {
	return e.meta
}

func (e *Error) WithMeta(meta http.Header) *Error {
	for key, vals := range meta {
		if len(vals) == 0 {
			// For response trailers, net/http will pre-populate entries
			// with nil values based on the "Trailer" header. But if there
			// are no actual values for those keys, we skip them.
			continue
		}
		e.meta[key] = append(e.meta[key], vals...)
	}
	return e
}
