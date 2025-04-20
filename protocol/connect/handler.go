package connect

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
)

var _ protocol.ProtocolHandler = (*connectHandler)(nil)

type connectHandler struct {
	protocol.ProtocolHandlerParams

	methods map[string]struct{}
	accept  map[string]struct{}
}

func (h *connectHandler) Methods() map[string]struct{} {
	return h.methods
}

func (h *connectHandler) ContentTypes() map[string]struct{} {
	return h.accept
}

func (*connectHandler) SetTimeout(request *http.Request) (context.Context, context.CancelFunc, error) {
	timeout := headers.GetHeaderCanonical(request.Header, connectHeaderTimeout)
	if timeout == "" {
		return request.Context(), nil, nil
	}
	if len(timeout) > 10 {
		return nil, nil, errors.Newf("parse timeout: %q has >10 digits", timeout).WithCode(errors.InvalidArgument)
	}
	millis, err := strconv.ParseInt(timeout, 10 /* base */, 64 /* bitsize */)
	if err != nil {
		return nil, nil, errors.Newf("parse timeout: %w", err).WithCode(errors.InvalidArgument)
	}
	ctx, cancel := context.WithTimeout(
		request.Context(),
		time.Duration(millis)*time.Millisecond,
	)
	return ctx, cancel, nil
}

func (h *connectHandler) CanHandlePayload(request *http.Request, contentType string) bool {
	if request.Method == http.MethodGet {
		query := request.URL.Query()
		codecName := query.Get(connectUnaryEncodingQueryParameter)
		contentType = connectContentTypeFromCodecName(
			h.Spec.StreamType,
			codecName,
		)
	}
	_, ok := h.accept[contentType]
	return ok
}

func (h *connectHandler) NewConn(
	responseWriter http.ResponseWriter,
	request *http.Request,
) (protocol.HandlerConnCloser, bool) {
	ctx := request.Context()
	query := request.URL.Query()
	// We need to parse metadata before entering the interceptor stack; we'll
	// send the error to the client later on.
	var contentEncoding, acceptEncoding string
	if h.Spec.StreamType == protocol.StreamTypeUnary {
		if request.Method == http.MethodGet {
			contentEncoding = query.Get(connectUnaryCompressionQueryParameter)
		} else {
			contentEncoding = headers.GetHeaderCanonical(request.Header, connectUnaryHeaderCompression)
		}
		acceptEncoding = headers.GetHeaderCanonical(request.Header, connectUnaryHeaderAcceptCompression)
	} else {
		contentEncoding = headers.GetHeaderCanonical(request.Header, connectStreamingHeaderCompression)
		acceptEncoding = headers.GetHeaderCanonical(request.Header, connectStreamingHeaderAcceptCompression)
	}
	requestCompression, responseCompression, failed := compress.NegotiateCompression(
		h.CompressionPools,
		contentEncoding,
		acceptEncoding,
	)
	if failed == nil {
		failed = protocol.CheckServerStreamsCanFlush(h.Spec.StreamType, responseWriter)
	}

	var requestBody io.ReadCloser
	var contentType, codecName string
	if request.Method == http.MethodGet {
		if failed == nil && !query.Has(connectUnaryEncodingQueryParameter) {
			failed = errors.Newf("missing %s parameter", connectUnaryEncodingQueryParameter).WithCode(errors.InvalidArgument)
		} else if failed == nil && !query.Has(connectUnaryMessageQueryParameter) {
			failed = errors.Newf("missing %s parameter", connectUnaryMessageQueryParameter).WithCode(errors.InvalidArgument)
		}
		msg := query.Get(connectUnaryMessageQueryParameter)
		msgReader := queryValueReader(msg, query.Get(connectUnaryBase64QueryParameter) == "1")
		requestBody = io.NopCloser(msgReader)
		codecName = query.Get(connectUnaryEncodingQueryParameter)
		contentType = connectContentTypeFromCodecName(
			h.Spec.StreamType,
			codecName,
		)
	} else {
		requestBody = request.Body
		contentType = headers.GetHeaderCanonical(request.Header, headers.HeaderContentType)
		codecName = connectCodecFromContentType(
			h.Spec.StreamType,
			contentType,
		)
	}

	codec := h.Codecs.Get(codecName)
	// The codec can be nil in the GET request case; that's okay: when failed
	// is non-nil, codec is never used.
	if failed == nil && codec == nil {
		failed = errors.Newf("invalid message encoding: %q", codecName).WithCode(errors.InvalidArgument)
	}

	// Write any remaining headers here:
	// (1) any writes to the stream will implicitly send the headers, so we
	// should get all of gRPC's required response headers ready.
	// (2) interceptors should be able to see these headers.
	//
	// Since we know that these header keys are already in canonical form, we can
	// skip the normalization in Header.Set.
	header := responseWriter.Header()
	header[headers.HeaderContentType] = []string{contentType}
	acceptCompressionHeader := connectUnaryHeaderAcceptCompression
	if h.Spec.StreamType != protocol.StreamTypeUnary {
		acceptCompressionHeader = connectStreamingHeaderAcceptCompression
		// We only write the request encoding header here for streaming calls,
		// since the streaming envelope lets us choose whether to compress each
		// message individually. For unary, we won't know whether we're compressing
		// the request until we see how large the payload is.
		if responseCompression != compress.CompressionIdentity {
			header[connectStreamingHeaderCompression] = []string{responseCompression}
		}
	}
	header[acceptCompressionHeader] = []string{h.CompressionPools.CommaSeparatedNames()}

	var conn protocol.HandlerConnCloser
	peer := protocol.Peer{
		Addr:     request.RemoteAddr,
		Protocol: ProtocolConnect,
		Query:    query,
	}
	if h.Spec.StreamType == protocol.StreamTypeUnary {
		conn = &connectUnaryHandlerConn{
			spec:           h.Spec,
			peer:           peer,
			request:        request,
			responseWriter: responseWriter,
			marshaler: connectUnaryMarshaler{
				ctx:              ctx,
				sender:           envelope.WriteSender{Writer: responseWriter},
				codec:            codec,
				compressMinBytes: h.CompressMinBytes,
				compressionName:  responseCompression,
				compressionPool:  h.CompressionPools.Get(responseCompression),
				bufferPool:       h.BufferPool,
				header:           responseWriter.Header(),
				sendMaxBytes:     h.SendMaxBytes,
			},
			unmarshaler: connectUnaryUnmarshaler{
				ctx:             ctx,
				reader:          requestBody,
				codec:           codec,
				compressionName: requestCompression,
				compressionPool: h.CompressionPools.Get(requestCompression),
				bufferPool:      h.BufferPool,
				readMaxBytes:    h.ReadMaxBytes,
			},
			responseTrailer: make(http.Header),
		}
	} else {
		conn = &connectStreamingHandlerConn{
			spec:           h.Spec,
			peer:           peer,
			request:        request,
			responseWriter: responseWriter,
			marshaler: connectStreamingMarshaler{
				EnvelopeWriter: envelope.EnvelopeWriter{
					Ctx:              ctx,
					Sender:           envelope.WriteSender{Writer: responseWriter},
					Codec:            codec,
					CompressMinBytes: h.CompressMinBytes,
					CompressionName:  responseCompression,
					CompressionPool:  h.CompressionPools.Get(responseCompression),
					BufferPool:       h.BufferPool,
					SendMaxBytes:     h.SendMaxBytes,
				},
			},
			unmarshaler: connectStreamingUnmarshaler{
				EnvelopeReader: envelope.EnvelopeReader{
					Ctx:             ctx,
					Reader:          requestBody,
					Codec:           codec,
					CompressionName: requestCompression,
					CompressionPool: h.CompressionPools.Get(requestCompression),
					BufferPool:      h.BufferPool,
					ReadMaxBytes:    h.ReadMaxBytes,
				},
			},
			responseTrailer: make(http.Header),
		}
	}
	conn = protocol.WrapHandlerConnWithCodedErrors(conn)

	if failed != nil {
		// Negotiation failed, so we can't establish a stream.
		_ = conn.Close(failed)
		return nil, false
	}
	return conn, true
}
