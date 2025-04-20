package grpc

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/mem"
	"github.com/opensraph/srpc/protocol"
)

var errNoTimeout = errors.New("no timeout")

var _ protocol.ProtocolHandler = (*grpcHandler)(nil)

type grpcHandler struct {
	protocol.ProtocolHandlerParams

	web    bool
	accept map[string]struct{}
}

// NewConn implements transport.ProtocolHandler.
func (g *grpcHandler) NewConn(responseWriter http.ResponseWriter, request *http.Request) (protocol.HandlerConnCloser, bool) {
	ctx := request.Context()
	// We need to parse metadata before entering the interceptor stack; we'll
	// send the error to the client later on.
	requestCompression, responseCompression, failed := compress.NegotiateCompression(
		g.CompressionPools,
		headers.GetHeaderCanonical(request.Header, grpcHeaderCompression),
		headers.GetHeaderCanonical(request.Header, grpcHeaderAcceptCompression),
	)
	if failed == nil {
		failed = protocol.CheckServerStreamsCanFlush(g.Spec.StreamType, responseWriter)
	}

	// Write any remaining headers here:
	// (1) any writes to the stream will implicitly send the headers, so we
	// should get all of gRPC's required response headers ready.
	// (2) interceptors should be able to see these headers.
	//
	// Since we know that these header keys are already in canonical form, we can
	// skip the normalization in Header.Set.
	header := responseWriter.Header()
	header[headers.HeaderContentType] = []string{headers.GetHeaderCanonical(request.Header, headers.HeaderContentType)}
	header[grpcHeaderAcceptCompression] = []string{g.CompressionPools.CommaSeparatedNames()}
	if responseCompression != compress.CompressionIdentity {
		header[grpcHeaderCompression] = []string{responseCompression}
	}

	codecName := grpcCodecFromContentType(g.web, headers.GetHeaderCanonical(request.Header, headers.HeaderContentType))
	cc, ok := g.Codecs[codecName] // handler.go guarantees this is not nil
	if !ok {
		return nil, false
	}
	protocolName := ProtocolGRPC
	if g.web {
		protocolName = ProtocolGRPCWeb
	}
	conn := protocol.WrapHandlerConnWithCodedErrors(&grpcHandlerConn{
		spec: g.Spec,
		peer: protocol.Peer{
			Addr:     request.RemoteAddr,
			Protocol: protocolName,
		},
		web:             g.web,
		bufferPool:      g.BufferPool,
		protobuf:        g.Codecs[encoding.CodecNameProto], // for errors
		responseWriter:  responseWriter,
		responseHeader:  make(http.Header),
		responseTrailer: make(http.Header),
		request:         request,
		marshaler: grpcMarshaler{
			EnvelopeWriter: envelope.EnvelopeWriter{
				Ctx:              ctx,
				Sender:           envelope.WriteSender{Writer: responseWriter},
				CompressionName:  responseCompression,
				CompressionPool:  g.CompressionPools.Get(responseCompression),
				Codec:            cc,
				CompressMinBytes: g.CompressMinBytes,
				BufferPool:       g.BufferPool,
				SendMaxBytes:     g.SendMaxBytes,
			},
		},
		unmarshaler: grpcUnmarshaler{
			EnvelopeReader: envelope.EnvelopeReader{
				Ctx:             ctx,
				Reader:          request.Body,
				Codec:           cc,
				CompressionName: requestCompression,
				CompressionPool: g.CompressionPools.Get(requestCompression),
				BufferPool:      g.BufferPool,
				ReadMaxBytes:    g.ReadMaxBytes,
			},
			web: g.web,
		},
	})
	if failed != nil {
		// Negotiation failed, so we can't establish a stream.
		_ = conn.Close(failed)
		return nil, false
	}
	return conn, true
}

// CanHandlePayload implements transport.ProtocolHandler.
func (g *grpcHandler) CanHandlePayload(_ *http.Request, contentType string) bool {
	_, ok := g.accept[contentType]
	return ok
}

// ContentTypes implements transport.ProtocolHandler.
func (g *grpcHandler) ContentTypes() map[string]struct{} {
	return g.accept
}

// Methods implements transport.ProtocolHandler.
func (g *grpcHandler) Methods() map[string]struct{} {
	return grpcAllowedMethods
}

// SetTimeout implements transport.ProtocolHandler.
func (g *grpcHandler) SetTimeout(request *http.Request) (context.Context, context.CancelFunc, error) {
	timeout, err := grpcParseTimeout(headers.GetHeaderCanonical(request.Header, grpcHeaderTimeout))
	if err != nil && !errors.Is(err, errNoTimeout) {
		// Errors here indicate that the client sent an invalid timeout header, so
		// the error text is safe to send back.
		return nil, nil, errors.FromError(err).WithCode(errors.InvalidArgument)
	} else if err != nil {
		// err wraps errNoTimeout, nothing to do.
		return request.Context(), nil, nil //nolint:nilerr
	}
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	return ctx, cancel, nil
}

var _ protocol.HandlerConnCloser = (*grpcHandlerConn)(nil)

type grpcHandlerConn struct {
	spec            protocol.Spec
	peer            protocol.Peer
	web             bool
	bufferPool      mem.BufferPool
	protobuf        encoding.Codec // for errors
	responseWriter  http.ResponseWriter
	responseHeader  http.Header
	responseTrailer http.Header
	wroteToBody     bool
	request         *http.Request
	marshaler       grpcMarshaler
	unmarshaler     grpcUnmarshaler
}

// Close implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) Close(err error) (retErr error) {
	defer func() {
		// We don't want to copy unread portions of the body to /dev/null here: if
		// the client hasn't closed the request body, we'll block until the server
		// timeout kicks in. This could happen because the client is malicious, but
		// a well-intentioned client may just not expect the server to be returning
		// an error for a streaming RPC. Better to accept that we can't always reuse
		// TCP connections.
		closeErr := hc.request.Body.Close()
		if retErr == nil {
			retErr = closeErr
		}
	}()
	defer protocol.FlushResponseWriter(hc.responseWriter)
	// If we haven't written the headers yet, do so.
	if !hc.wroteToBody {
		headers.MergeHeaders(hc.responseWriter.Header(), hc.responseHeader)
	}
	// gRPC always sends the error's code, message, details, and metadata as
	// trailing metadata. The Connect protocol doesn't do this, so we don't want
	// to mutate the trailers map that the user sees.
	mergedTrailers := make(
		http.Header,
		len(hc.responseTrailer)+2, // always make space for status & message
	)
	headers.MergeHeaders(mergedTrailers, hc.responseTrailer)
	grpcErrorToTrailer(mergedTrailers, hc.protobuf, err)
	if hc.web && !hc.wroteToBody && len(hc.responseHeader) == 0 {
		// We're using gRPC-Web, we haven't yet written to the body, and there are no
		// custom headers. That means we can send a "trailers-only" response and send
		// trailing metadata as HTTP headers (instead of as trailers).
		headers.MergeHeaders(hc.responseWriter.Header(), mergedTrailers)
		return nil
	}
	if hc.web {
		// We're using gRPC-Web and we've already sent the headers, so we write
		// trailing metadata to the HTTP body.
		if err := hc.marshaler.MarshalWebTrailers(mergedTrailers); err != nil {
			return err
		}
		return nil // must be a literal nil: nil *Error is a non-nil error
	}
	// We're using standard gRPC. Even if we haven't written to the body and
	// we're sending a "trailers-only" response, we must send trailing metadata
	// as HTTP trailers. (If we had frame-level control of the HTTP/2 layer, we
	// could send trailers-only responses as a single HEADER frame and no DATA
	// frames, but net/http doesn't expose APIs that low-level.)
	//
	// In net/http's ResponseWriter API, we send HTTP trailers by writing to the
	// headers map with a special prefix. This prefixing is an implementation
	// detail, so we should hide it and _not_ mutate the user-visible headers.
	//
	// Note that this is _very_ finicky and difficult to test with net/http,
	// since correctness depends on low-level framing details. Breaking this
	// logic breaks Envoy's gRPC-Web translation.
	for key, values := range mergedTrailers {
		for _, value := range values {
			// These are potentially user-supplied, so we can't assume they're in
			// canonical form.
			hc.responseWriter.Header().Add(http.TrailerPrefix+key, value)
		}
	}
	return nil
}

// Spec implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) Spec() protocol.Spec {
	return hc.spec
}

// Peer implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) Peer() protocol.Peer {
	return hc.peer
}

// Receive implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) Receive(msg any) error {
	if err := hc.unmarshaler.Unmarshal(msg); err != nil {
		return err // already coded
	}
	return nil // must be a literal nil: nil *Error is a non-nil error
}

// RequestHeader implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) RequestHeader() http.Header {
	return hc.request.Header
}

// ResponseHeader implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) ResponseHeader() http.Header {
	return hc.responseHeader
}

// ResponseTrailer implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) ResponseTrailer() http.Header {
	return hc.responseTrailer
}

// Send implements spec.HandlerConnCloser.
func (hc *grpcHandlerConn) Send(msg any) error {
	defer protocol.FlushResponseWriter(hc.responseWriter)
	if !hc.wroteToBody {
		headers.MergeHeaders(hc.responseWriter.Header(), hc.responseHeader)
		hc.wroteToBody = true
	}
	if err := hc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil *Error is a non-nil error
}

func grpcParseTimeout(timeout string) (time.Duration, error) {
	if timeout == "" {
		return 0, errNoTimeout
	}
	unit, err := grpcTimeoutUnitLookup(timeout[len(timeout)-1])
	if err != nil {
		return 0, err
	}
	num, err := strconv.ParseInt(timeout[:len(timeout)-1], 10 /* base */, 64 /* bitsize */)
	if err != nil || num < 0 {
		return 0, fmt.Errorf("protocol error: invalid timeout %q", timeout)
	}
	if num > 99999999 { // timeout must be ASCII string of at most 8 digits
		return 0, fmt.Errorf("protocol error: timeout %q is too long", timeout)
	}
	const grpcTimeoutMaxHours = math.MaxInt64 / int64(time.Hour) // how many hours fit into a time.Duration?
	if unit == time.Hour && num > grpcTimeoutMaxHours {
		// Timeout is effectively unbounded, so ignore it. The grpc-go
		// implementation does the same thing.
		return 0, errNoTimeout
	}
	return time.Duration(num) * unit, nil
}

func grpcTimeoutUnitLookup(unit byte) (time.Duration, error) {
	switch unit {
	case 'n':
		return time.Nanosecond, nil
	case 'u':
		return time.Microsecond, nil
	case 'm':
		return time.Millisecond, nil
	case 'S':
		return time.Second, nil
	case 'M':
		return time.Minute, nil
	case 'H':
		return time.Hour, nil
	default:
		return 0, fmt.Errorf("protocol error: timeout has invalid unit %q", unit)
	}
}

func grpcCodecFromContentType(web bool, contentType string) string {
	if (!web && contentType == grpcContentTypeDefault) || (web && contentType == grpcWebContentTypeDefault) {
		// implicitly protobuf
		return encoding.CodecNameProto
	}
	prefix := grpcContentTypePrefix
	if web {
		prefix = grpcWebContentTypePrefix
	}
	return strings.TrimPrefix(contentType, prefix)
}
