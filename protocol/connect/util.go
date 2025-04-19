package connect

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/protocol"
)

type connectEndStreamMessage struct {
	Error   *connectWireError `json:"error,omitempty"`
	Trailer http.Header       `json:"metadata,omitempty"`
}

func connectCodeToHTTP(code errors.Code) int {
	// Return literals rather than named constants from the HTTP package to make
	// it easier to compare this function to the Connect specification.
	switch code {
	case errors.Canceled:
		return 499
	case errors.Unknown:
		return 500
	case errors.InvalidArgument:
		return 400
	case errors.DeadlineExceeded:
		return 504
	case errors.NotFound:
		return 404
	case errors.AlreadyExists:
		return 409
	case errors.PermissionDenied:
		return 403
	case errors.ResourceExhausted:
		return 429
	case errors.FailedPrecondition:
		return 400
	case errors.Aborted:
		return 409
	case errors.OutOfRange:
		return 400
	case errors.Unimplemented:
		return 501
	case errors.Internal:
		return 500
	case errors.Unavailable:
		return 503
	case errors.DataLoss:
		return 500
	case errors.Unauthenticated:
		return 401
	default:
		return 500 // same as CodeUnknown
	}
}

func connectCodecFromContentType(streamType protocol.StreamType, contentType string) string {
	if streamType == protocol.StreamTypeUnary {
		return strings.TrimPrefix(contentType, connectUnaryContentTypePrefix)
	}
	return strings.TrimPrefix(contentType, connectStreamingContentTypePrefix)
}

func connectContentTypeFromCodecName(streamType protocol.StreamType, name string) string {
	if streamType == protocol.StreamTypeUnary {
		return connectUnaryContentTypePrefix + name
	}
	return connectStreamingContentTypePrefix + name
}

// encodeBinaryQueryValue URL-safe base64-encodes data, without padding.
func encodeBinaryQueryValue(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// binaryQueryValueReader creates a reader that can read either padded or
// unpadded URL-safe base64 from a string.
func binaryQueryValueReader(data string) io.Reader {
	stringReader := strings.NewReader(data)
	if len(data)%4 != 0 {
		// Data definitely isn't padded.
		return base64.NewDecoder(base64.RawURLEncoding, stringReader)
	}
	// Data is padded, or no padding was necessary.
	return base64.NewDecoder(base64.URLEncoding, stringReader)
}

// queryValueReader creates a reader for a string that may be URL-safe base64
// encoded.
func queryValueReader(data string, base64Encoded bool) io.Reader {
	if base64Encoded {
		return binaryQueryValueReader(data)
	}
	return strings.NewReader(data)
}

func connectValidateUnaryResponseContentType(
	requestCodecName string,
	httpMethod string,
	statusCode int,
	statusMsg string,
	responseContentType string,
) error {
	if statusCode != http.StatusOK {
		if statusCode == http.StatusNotModified && httpMethod == http.MethodGet {
			return errors.NewWireError(errors.Unknown, errors.ErrNotModifiedClient)
		}
		// Error responses must be JSON-encoded.
		if responseContentType == connectUnaryContentTypePrefix+encoding.CodecNameJSON ||
			responseContentType == connectUnaryContentTypePrefix+encoding.CodecNameJSONCharsetUTF8 {
			return nil
		}
		return errors.FromError(
			errors.New(statusMsg),
		).WithCode(errors.HttpToCode(statusCode))
	}
	// Normal responses must have valid content-type that indicates same codec as the request.
	if !strings.HasPrefix(responseContentType, connectUnaryContentTypePrefix) {
		// Doesn't even look like a Connect response? Use code "unknown".
		return errors.Newf(
			"invalid content-type: %q; expecting %q",
			responseContentType,
			connectUnaryContentTypePrefix+requestCodecName,
		).WithCode(errors.Unknown)
	}
	responseCodecName := connectCodecFromContentType(
		protocol.StreamTypeUnary,
		responseContentType,
	)
	if responseCodecName == requestCodecName {
		return nil
	}
	// HACK: We likely want a better way to handle the optional "charset" parameter
	//       for application/json, instead of hard-coding. But this suffices for now.
	if (responseCodecName == encoding.CodecNameJSON && requestCodecName == encoding.CodecNameJSONCharsetUTF8) ||
		(responseCodecName == encoding.CodecNameJSONCharsetUTF8 && requestCodecName == encoding.CodecNameJSON) {
		// Both are JSON
		return nil
	}
	return errors.Newf(
		"invalid content-type: %q; expecting %q",
		responseContentType,
		connectUnaryContentTypePrefix+requestCodecName,
	).WithCode(errors.Internal)
}

func connectValidateStreamResponseContentType(requestCodecName string, streamType protocol.StreamType, responseContentType string) error {
	// Responses must have valid content-type that indicates same codec as the request.
	if !strings.HasPrefix(responseContentType, connectStreamingContentTypePrefix) {
		// Doesn't even look like a Connect response? Use code "unknown".
		return errors.Newf(
			"invalid content-type: %q; expecting %q",
			responseContentType,
			connectStreamingContentTypePrefix+requestCodecName,
		).WithCode(errors.Unknown)
	}
	responseCodecName := connectCodecFromContentType(
		streamType,
		responseContentType,
	)
	if responseCodecName != requestCodecName {
		return errors.Newf(
			"invalid content-type: %q; expecting %q",
			responseContentType,
			connectStreamingContentTypePrefix+requestCodecName,
		).WithCode(errors.Internal)
	}
	return nil
}
