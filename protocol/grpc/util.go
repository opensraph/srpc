package grpc

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/mem"

	spb "google.golang.org/genproto/googleapis/rpc/status"
)

func grpcEncodeTimeout(timeout time.Duration) string {
	if timeout <= 0 {
		return "0n"
	}
	// The gRPC protocol limits timeouts to 8 characters (not counting the unit),
	// so timeouts must be strictly less than 1e8 of the appropriate unit.
	const grpcTimeoutMaxValue = 1e8
	var (
		size time.Duration
		unit byte
	)
	switch {
	case timeout < time.Nanosecond*grpcTimeoutMaxValue:
		size, unit = time.Nanosecond, 'n'
	case timeout < time.Microsecond*grpcTimeoutMaxValue:
		size, unit = time.Microsecond, 'u'
	case timeout < time.Millisecond*grpcTimeoutMaxValue:
		size, unit = time.Millisecond, 'm'
	case timeout < time.Second*grpcTimeoutMaxValue:
		size, unit = time.Second, 'S'
	case timeout < time.Minute*grpcTimeoutMaxValue:
		size, unit = time.Minute, 'M'
	default:
		// time.Duration is an int64 number of nanoseconds, so the largest
		// expressible duration is less than 1e8 hours.
		size, unit = time.Hour, 'H'
	}
	buf := make([]byte, 0, 9)
	buf = strconv.AppendInt(buf, int64(timeout/size), 10 /* base */)
	buf = append(buf, unit)
	return string(buf)
}

func grpcContentTypeFromCodecName(web bool, name string) string {
	if web {
		return grpcWebContentTypePrefix + name
	}
	if name == encoding.CodecNameProto {
		// For compatibility with Google Cloud Platform's frontend, prefer an
		// implicit default codec. See
		// https://github.com/connectrpc/connect-go/pull/655#issuecomment-1915754523
		// for details.
		return grpcContentTypeDefault
	}
	return grpcContentTypePrefix + name
}

// grpcPercentEncode follows RFC 3986 Section 2.1 and the gRPC HTTP/2 spec.
// It's a variant of URL-encoding with fewer reserved characters. It's intended
// to take UTF-8 encoded text and escape non-ASCII bytes so that they're valid
// HTTP/1 headers, while still maximizing readability of the data on the wire.
//
// The grpc-message trailer (used for human-readable error messages) should be
// percent-encoded.
//
// References:
//
//	https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#responses
//	https://datatracker.ietf.org/doc/html/rfc3986#section-2.1
func grpcPercentEncode(msg string) string {
	var hexCount int
	for i := range len(msg) {
		if grpcShouldEscape(msg[i]) {
			hexCount++
		}
	}
	if hexCount == 0 {
		return msg
	}
	// We need to escape some characters, so we'll need to allocate a new string.
	var out strings.Builder
	out.Grow(len(msg) + 2*hexCount)
	for i := range len(msg) {
		switch char := msg[i]; {
		case grpcShouldEscape(char):
			out.WriteByte('%')
			out.WriteByte(upperHex[char>>4])
			out.WriteByte(upperHex[char&15])
		default:
			out.WriteByte(char)
		}
	}
	return out.String()
}

func grpcPercentDecode(input string) (string, error) {
	percentCount := 0
	for i := 0; i < len(input); {
		switch input[i] {
		case '%':
			percentCount++
			if err := validateHex(input[i:]); err != nil {
				return "", err
			}
			i += 3
		default:
			i++
		}
	}
	if percentCount == 0 {
		return input, nil
	}
	// We need to unescape some characters, so we'll need to allocate a new string.
	var out strings.Builder
	out.Grow(len(input) - 2*percentCount)
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '%':
			out.WriteByte(unHex(input[i+1])<<4 | unHex(input[i+2]))
			i += 2
		default:
			out.WriteByte(input[i])
		}
	}
	return out.String(), nil
}

func validateHex(input string) error {
	if len(input) < 3 || input[0] != '%' || !isHex(input[1]) || !isHex(input[2]) {
		if len(input) > 3 {
			input = input[:3]
		}
		return fmt.Errorf("invalid percent-encoded string %q", input)
	}
	return nil
}

func unHex(char byte) byte {
	switch {
	case '0' <= char && char <= '9':
		return char - '0'
	case 'a' <= char && char <= 'f':
		return char - 'a' + 10
	case 'A' <= char && char <= 'F':
		return char - 'A' + 10
	}
	return 0
}

func isHex(char byte) bool {
	return ('0' <= char && char <= '9') || ('a' <= char && char <= 'f') || ('A' <= char && char <= 'F')
}

// Characters that need to be escaped are defined in gRPC's HTTP/2 spec.
// They're different from the generic set defined in RFC 3986.
func grpcShouldEscape(char byte) bool {
	return char < ' ' || char > '~' || char == '%'
}

func grpcErrorToTrailer(trailer http.Header, protobuf encoding.Codec, err error) {
	if err == nil {
		headers.SetHeaderCanonical(trailer, grpcHeaderStatus, "0") // zero is the gRPC OK status
		return
	}
	var srpcErr *errors.Error
	if ok := errors.As(err, &srpcErr); ok && !srpcErr.IsWireError() {
		headers.MergeNonProtocolHeaders(trailer, srpcErr.Meta())
	}
	var (
		status  = grpcStatusFromError(err)
		code    = status.GetCode()
		message = status.GetMessage()
		bin     mem.BufferSlice
	)
	if len(status.Details) > 0 {
		var binErr error
		bin, binErr = protobuf.Marshal(status)
		if binErr != nil {
			code = int32(errors.Internal)
			message = fmt.Sprintf("marshal protobuf status: %v", binErr)
		}
	}
	headers.SetHeaderCanonical(trailer, grpcHeaderStatus, strconv.Itoa(int(code)))
	headers.SetHeaderCanonical(trailer, grpcHeaderMessage, grpcPercentEncode(message))
	if bin.Len() > 0 {
		headers.SetHeaderCanonical(trailer, grpcHeaderDetails, headers.EncodeBinaryHeader(bin.Materialize()))
	}
}

func grpcStatusFromError(err error) *spb.Status {
	status := &spb.Status{
		Code:    int32(errors.Unknown),
		Message: err.Error(),
	}
	if srpcErr, ok := errors.AsError(err); ok {
		status.Code = int32(srpcErr.Code()) //nolint:gosec // No information loss
		status.Message = srpcErr.Error()
		status.Details = srpcErr.DetailsAsAny()
	}
	return status
}
