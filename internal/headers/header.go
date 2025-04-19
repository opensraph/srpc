package headers

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strings"
)

const (
	HeaderContentType     = "Content-Type"
	HeaderContentEncoding = "Content-Encoding"
	HeaderContentLength   = "Content-Length"
	HeaderHost            = "Host"
	HeaderUserAgent       = "User-Agent"
	HeaderTrailer         = "Trailer"
	HeaderDate            = "Date"
)

const (
	ConnectUnaryHeaderCompression           = "Content-Encoding"
	ConnectUnaryHeaderAcceptCompression     = "Accept-Encoding"
	ConnectUnaryTrailerPrefix               = "Trailer-"
	ConnectStreamingHeaderCompression       = "Connect-Content-Encoding"
	ConnectStreamingHeaderAcceptCompression = "Connect-Accept-Encoding"
	ConnectHeaderTimeout                    = "Connect-Timeout-Ms"
	ConnectHeaderProtocolVersion            = "Connect-Protocol-Version"
	ConnectProtocolVersion                  = "1"
)

const (
	GRPCHeaderCompression       = "Grpc-Encoding"
	GRPCHeaderAcceptCompression = "Grpc-Accept-Encoding"
	GRPCHeaderTimeout           = "Grpc-Timeout"
	GRPCHeaderStatus            = "Grpc-Status"
	GRPCHeaderMessage           = "Grpc-Message"
	GRPCHeaderDetails           = "Grpc-Status-Details-Bin"
)

//nolint:gochecknoglobals
var ProtocolHeaders = map[string]struct{}{
	// HTTP headers.
	HeaderContentType:     {},
	HeaderContentLength:   {},
	HeaderContentEncoding: {},
	HeaderHost:            {},
	HeaderUserAgent:       {},
	HeaderTrailer:         {},
	HeaderDate:            {},
	// Connect headers.
	ConnectUnaryHeaderAcceptCompression:     {},
	ConnectUnaryTrailerPrefix:               {},
	ConnectStreamingHeaderCompression:       {},
	ConnectStreamingHeaderAcceptCompression: {},
	ConnectHeaderTimeout:                    {},
	ConnectHeaderProtocolVersion:            {},
	// gRPC headers.
	GRPCHeaderCompression:       {},
	GRPCHeaderAcceptCompression: {},
	GRPCHeaderTimeout:           {},
	GRPCHeaderStatus:            {},
	GRPCHeaderMessage:           {},
	GRPCHeaderDetails:           {},
}

// EncodeBinaryHeader base64-encodes the data. It always emits unpadded values.
//
// In the Connect, gRPC, and gRPC-Web protocols, binary headers must have keys
// ending in "-Bin".
func EncodeBinaryHeader(data []byte) string {
	// gRPC specification says that implementations should emit unpadded values.
	return base64.RawStdEncoding.EncodeToString(data)
}

// DecodeBinaryHeader base64-decodes the data. It can decode padded or unpadded
// values. Following usual HTTP semantics, multiple base64-encoded values may
// be joined with a comma. When receiving such comma-separated values, split
// them with [strings.Split] before calling DecodeBinaryHeader.
//
// Binary headers sent using the Connect, gRPC, and gRPC-Web protocols have
// keys ending in "-Bin".
func DecodeBinaryHeader(data string) ([]byte, error) {
	if len(data)%4 != 0 {
		// Data definitely isn't padded.
		return base64.RawStdEncoding.DecodeString(data)
	}
	// Either the data was padded, or padding wasn't necessary. In both cases,
	// the padding-aware decoder works.
	return base64.StdEncoding.DecodeString(data)
}

func MergeHeaders(into, from http.Header) {
	for key, vals := range from {
		if len(vals) == 0 {
			// For response trailers, net/http will pre-populate entries
			// with nil values based on the "Trailer" header. But if there
			// are no actual values for those keys, we skip them.
			continue
		}
		into[key] = append(into[key], vals...)
	}
}

// mergeNonProtocolHeaders merges headers excluding protocol headers defined in
// protocolHeaders.
func MergeNonProtocolHeaders(into, from http.Header) {
	for key, vals := range from {
		if len(vals) == 0 {
			// For response trailers, net/http will pre-populate entries
			// with nil values based on the "Trailer" header. But if there
			// are no actual values for those keys, we skip them.
			continue
		}
		if _, isProtocolHeader := ProtocolHeaders[key]; !isProtocolHeader {
			into[key] = append(into[key], vals...)
		}
	}
}

// getHeaderCanonical is a shortcut for Header.Get() which
// bypasses the CanonicalMIMEHeaderKey operation when we
// know the key is already in canonical form.
func GetHeaderCanonical(h http.Header, key string) string {
	if h == nil {
		return ""
	}
	v := h[key]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// setHeaderCanonical is a shortcut for Header.Set() which
// bypasses the CanonicalMIMEHeaderKey operation when we
// know the key is already in canonical form.
func SetHeaderCanonical(h http.Header, key, value string) {
	h[key] = []string{value}
}

// delHeaderCanonical is a shortcut for Header.Del() which
// bypasses the CanonicalMIMEHeaderKey operation when we
// know the key is already in canonical form.
func DelHeaderCanonical(h http.Header, key string) {
	delete(h, key)
}

func CanonicalizeContentType(contentType string) string {
	// Typically, clients send Content-Type in canonical form, without
	// parameters. In those cases, we'd like to avoid parsing and
	// canonicalization overhead.
	//
	// See https://www.rfc-editor.org/rfc/rfc2045.html#section-5.1 for a full
	// grammar.
	var slashes int
	for _, r := range contentType {
		switch {
		case r >= 'a' && r <= 'z':
		case r == '.' || r == '+' || r == '-':
		case r == '/':
			slashes++
		default:
			return canonicalizeContentTypeSlow(contentType)
		}
	}
	if slashes == 1 {
		return contentType
	}
	return canonicalizeContentTypeSlow(contentType)
}

func canonicalizeContentTypeSlow(contentType string) string {
	base, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType
	}
	// According to RFC 9110 Section 8.3.2, the charset parameter value should be treated as case-insensitive.
	// mime.FormatMediaType canonicalizes parameter names, but not parameter values,
	// because the case sensitivity of a parameter value depends on its semantics.
	// Therefore, the charset parameter value should be canonicalized here.
	// ref.) https://httpwg.org/specs/rfc9110.html#rfc.section.8.3.2
	if charset, ok := params["charset"]; ok {
		params["charset"] = strings.ToLower(charset)
	}
	return mime.FormatMediaType(base, params)
}
