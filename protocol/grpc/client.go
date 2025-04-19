package grpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/internal/utils"
	"github.com/opensraph/srpc/mem"
	"github.com/opensraph/srpc/protocol"
	spb "google.golang.org/genproto/googleapis/rpc/status"
)

var _ protocol.ProtocolClient = (*grpcClient)(nil)

type grpcClient struct {
	protocol.ProtocolClientParams
	web  bool
	peer protocol.Peer
}

// Peer implements protocol.ProtocolClient.
func (g *grpcClient) Peer() protocol.Peer {
	return g.peer
}

// WriteRequestHeader implements protocol.ProtocolClient.
func (g *grpcClient) WriteRequestHeader(_ protocol.StreamType, header http.Header) {
	// We know these header keys are in canonical form, so we can bypass all the
	// checks in Header.Set.
	if headers.GetHeaderCanonical(header, headers.HeaderUserAgent) == "" {
		header[headers.HeaderUserAgent] = []string{defaultGrpcUserAgent}
	}
	if g.web && headers.GetHeaderCanonical(header, headerXUserAgent) == "" {
		// The gRPC-Web pseudo-specification seems to require X-User-Agent rather
		// than User-Agent for all clients, even if they're not browser-based. This
		// is very odd for a backend client, so we'll split the difference and set
		// both.
		header[headerXUserAgent] = []string{defaultGrpcUserAgent}
	}
	header[headers.HeaderContentType] = []string{grpcContentTypeFromCodecName(g.web, g.Codec.Name())}
	// gRPC handles compression on a per-message basis, so we don't want to
	// compress the whole stream. By default, http.Client will ask the server
	// to gzip the stream if we don't set Accept-Encoding.
	header["Accept-Encoding"] = []string{compress.CompressionIdentity}
	if g.CompressionName != "" && g.CompressionName != compress.CompressionIdentity {
		header[grpcHeaderCompression] = []string{g.CompressionName}
	}
	if acceptCompression := g.CompressionPools.CommaSeparatedNames(); acceptCompression != "" {
		header[grpcHeaderAcceptCompression] = []string{acceptCompression}
	}
	if !g.web {
		// The gRPC-HTTP2 specification requires this - it flushes out proxies that
		// don't support HTTP trailers.
		header["Te"] = []string{"trailers"}
	}
}

// NewConn implements protocol.ProtocolClient.
func (g *grpcClient) NewConn(ctx context.Context, spec protocol.Spec, header http.Header) protocol.StreamingClientConn {
	if deadline, ok := ctx.Deadline(); ok {
		encodedDeadline := grpcEncodeTimeout(time.Until(deadline))
		header[grpcHeaderTimeout] = []string{encodedDeadline}
	}
	duplexCall := duplex.NewDuplexHTTPCall(ctx, g.HTTPClient, g.URL, protocol.IsSendUnary(spec.StreamType), header)
	conn := &grpcClientConn{
		spec:             spec,
		peer:             g.Peer(),
		duplexCall:       duplexCall,
		compressionPools: g.CompressionPools,
		bufferPool:       g.BufferPool,
		protobuf:         g.Protobuf,
		marshaler: grpcMarshaler{
			EnvelopeWriter: envelope.EnvelopeWriter{
				Ctx:              ctx,
				Sender:           duplexCall,
				CompressionPool:  g.CompressionPools.Get(g.CompressionName),
				Codec:            g.Codec,
				CompressMinBytes: g.CompressMinBytes,
				BufferPool:       g.BufferPool,
				SendMaxBytes:     g.SendMaxBytes,
			},
		},
		unmarshaler: grpcUnmarshaler{
			EnvelopeReader: envelope.EnvelopeReader{
				Ctx:          ctx,
				Reader:       duplexCall,
				Codec:        g.Codec,
				BufferPool:   g.BufferPool,
				ReadMaxBytes: g.ReadMaxBytes,
			},
		},
		responseHeader:  make(http.Header),
		responseTrailer: make(http.Header),
	}
	duplexCall.SetValidateResponse(conn.validateResponse)

	if g.web {
		conn.unmarshaler.web = true
		conn.readTrailers = func(unmarshaler *grpcUnmarshaler, _ *duplex.DuplexHTTPCall) http.Header {
			return unmarshaler.WebTrailer()
		}
	} else {
		conn.readTrailers = func(_ *grpcUnmarshaler, call *duplex.DuplexHTTPCall) http.Header {
			// To access HTTP trailers, we need to read the body to EOF.
			_, _ = utils.Discard(call)
			return call.ResponseTrailer()
		}
	}
	return protocol.WrapClientConnWithCodedErrors(conn)
}

var _ protocol.ProtocolClient = (*grpcClient)(nil)

// grpcClientConn works for both gRPC and gRPC-Web.
type grpcClientConn struct {
	spec             protocol.Spec
	peer             protocol.Peer
	duplexCall       *duplex.DuplexHTTPCall
	compressionPools compress.ReadOnlyCompressionPools
	bufferPool       mem.BufferPool
	protobuf         encoding.Codec // for errors
	marshaler        grpcMarshaler
	unmarshaler      grpcUnmarshaler
	responseHeader   http.Header
	responseTrailer  http.Header
	readTrailers     func(*grpcUnmarshaler, *duplex.DuplexHTTPCall) http.Header
}

func (cc *grpcClientConn) Spec() protocol.Spec {
	return cc.spec
}

func (cc *grpcClientConn) Peer() protocol.Peer {
	return cc.peer
}

func (cc *grpcClientConn) Send(msg any) error {
	if err := cc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil *Error is a non-nil error
}

func (cc *grpcClientConn) RequestHeader() http.Header {
	return cc.duplexCall.Header()
}

func (cc *grpcClientConn) CloseRequest() error {
	return cc.duplexCall.CloseWrite()
}

func (cc *grpcClientConn) Receive(msg any) error {
	if err := cc.duplexCall.BlockUntilResponseReady(); err != nil {
		return err
	}
	err := cc.unmarshaler.Unmarshal(msg)
	if err == nil {
		return nil
	}
	headers.MergeHeaders(
		cc.responseTrailer,
		cc.readTrailers(&cc.unmarshaler, cc.duplexCall),
	)
	if errors.Is(err, io.EOF) && cc.unmarshaler.BytesRead == 0 && len(cc.responseTrailer) == 0 {
		// No body and no trailers means a trailers-only response.
		// Note: per the specification, only the HTTP status code and Content-Type
		// should be treated as headers. The rest should be treated as trailing
		// metadata. But it would be unsafe to mutate cc.responseHeader at this
		// point. So we'll leave cc.responseHeader alone but copy the relevant
		// metadata into cc.responseTrailer.
		headers.MergeHeaders(cc.responseTrailer, cc.responseHeader)
		headers.DelHeaderCanonical(cc.responseTrailer, headers.HeaderContentType)

		// Try to read the status out of the headers.
		serverErr := cc.grpcErrorFromTrailer(cc.protobuf, cc.responseHeader)
		if serverErr == nil {
			// Status says "OK". So return original error (io.EOF).
			return err
		}

		if serverErr, ok := errors.AsError(serverErr); ok {
			serverErr.WithMeta(cc.responseHeader.Clone())
		}
		return serverErr
	}

	// See if the server sent an explicit error in the HTTP or gRPC-Web trailers.
	serverErr := cc.grpcErrorFromTrailer(cc.protobuf, cc.responseTrailer)
	if serverErr != nil && (errors.Is(err, io.EOF) || !errors.Is(serverErr, errTrailersWithoutGRPCStatus)) {
		// We've either:
		//   - Cleanly read until the end of the response body and *not* received
		//   gRPC status trailers, which is a protocol error, or
		//   - Received an explicit error from the server.
		//
		// This is expected from a protocol perspective, but receiving trailers
		// means that we're _not_ getting a message. For users to realize that
		// the stream has ended, Receive must return an error.
		if serverErr, ok := errors.AsError(serverErr); ok {
			serverErr.WithMeta(cc.responseHeader.Clone())
			serverErr.WithMeta(cc.responseTrailer.Clone())
		}
		_ = cc.duplexCall.CloseWrite()
		return serverErr
	}
	// This was probably an error converting the bytes to a message or an error
	// reading from the network. We're going to return it to the
	// user, but we also want to close writes so Send errors out.
	_ = cc.duplexCall.CloseWrite()
	return err
}

func (cc *grpcClientConn) ResponseHeader() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseHeader
}

func (cc *grpcClientConn) ResponseTrailer() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseTrailer
}

func (cc *grpcClientConn) CloseResponse() error {
	return cc.duplexCall.CloseRead()
}

func (cc *grpcClientConn) OnRequestSend(fn func(*http.Request)) {
	cc.duplexCall.OnRequestSend = fn
}

func (cc *grpcClientConn) validateResponse(response *http.Response) error {
	if err := grpcValidateResponse(
		response,
		cc.responseHeader,
		cc.compressionPools,
		cc.unmarshaler.web,
		cc.marshaler.Codec.Name(),
	); err != nil {
		return err
	}
	compression := headers.GetHeaderCanonical(response.Header, grpcHeaderCompression)
	cc.unmarshaler.CompressionPool = cc.compressionPools.Get(compression)
	return nil
}

// The gRPC wire protocol specifies that errors should be serialized using the
// binary Protobuf format, even if the messages in the request/response stream
// use a different codec. Consequently, this function needs a Protobuf codec to
// unmarshal error information in the headers.
//
// A nil error is only returned when a grpc-status key IS present, but it
// indicates a code of zero (no error). If no grpc-status key is present, this
// returns a non-nil *Error that wraps errTrailersWithoutGRPCStatus.
func (cc *grpcClientConn) grpcErrorFromTrailer(protobuf encoding.Codec, trailer http.Header) error {
	codeHeader := headers.GetHeaderCanonical(trailer, grpcHeaderStatus)
	if codeHeader == "" {
		// If there are no trailers at all, that's an internal error.
		// But if it's an error determining the status code from the
		// trailers, it's unknown.
		code := errors.Unknown
		if len(trailer) == 0 {
			code = errors.Internal
		}
		return errors.FromError(errTrailersWithoutGRPCStatus).WithCode(code)
	}
	if codeHeader == "0" {
		return nil
	}

	code, err := strconv.ParseUint(codeHeader, 10 /* base */, 32 /* bitsize */)
	if err != nil {
		return errors.Newf("protocol error: invalid error code %q", codeHeader).WithCode(errors.Unknown)
	}
	message, err := grpcPercentDecode(headers.GetHeaderCanonical(trailer, grpcHeaderMessage))
	if err != nil {
		return errors.Newf("protocol error: invalid error message %q", message).WithCode(errors.Internal)
	}

	retErr := errors.NewWireError(errors.Code(code), fmt.Errorf(message))

	detailsBinaryEncoded := headers.GetHeaderCanonical(trailer, grpcHeaderDetails)
	if len(detailsBinaryEncoded) > 0 {
		detailsBinary, err := headers.DecodeBinaryHeader(detailsBinaryEncoded)
		if err != nil {
			return errors.Newf("server returned invalid grpc-status-details-bin trailer: %w", err).WithCode(errors.Internal)
		}
		var status spb.Status
		bs := mem.BufferSlice{mem.NewBuffer(&detailsBinary, cc.bufferPool)}
		defer bs.Free()
		if err := protobuf.Unmarshal(bs, &status); err != nil {
			return errors.Newf("server returned invalid protobuf for error details: %w", err).WithCode(errors.Internal)
		}
		retErr = errors.NewWireError(errors.Code(status.Code), fmt.Errorf(status.Message))
		for _, d := range status.GetDetails() {
			retErr.WithDetails(d)
		}
	}
	return retErr
}

func grpcValidateResponse(
	response *http.Response,
	header http.Header,
	availableCompressors compress.ReadOnlyCompressionPools,
	web bool,
	codecName string,
) error {
	if response.StatusCode != http.StatusOK {
		return errors.Newf(
			"HTTP status %v",
			response.StatusCode,
		).WithCode(errors.HttpToCode(response.StatusCode))
	}
	if err := grpcValidateResponseContentType(
		web,
		codecName,
		headers.GetHeaderCanonical(response.Header, headers.HeaderContentType),
	); err != nil {
		return err
	}
	if compressionType := headers.GetHeaderCanonical(response.Header, grpcHeaderCompression); compressionType != "" &&
		compressionType != compress.CompressionIdentity &&
		!availableCompressors.Contains(compressionType) {
		// Per https://github.com/grpc/grpc/blob/master/doc/compression.md, we
		// should return CodeInternal and specify acceptable compression(s) (in
		// addition to setting the Grpc-Accept-Encoding header).
		return errors.Newf(
			"invalid compression %q: accepted encodings are %v",
			compressionType,
			availableCompressors.CommaSeparatedNames(),
		).WithCode(errors.Internal)
	}
	// The response is valid, so we should expose the headers.
	headers.MergeHeaders(header, response.Header)
	return nil
}

func grpcValidateResponseContentType(web bool, requestCodecName string, responseContentType string) error {
	// Responses must have valid content-type that indicates same codec as the request.
	bare, prefix := grpcContentTypeDefault, grpcContentTypePrefix
	if web {
		bare, prefix = grpcWebContentTypeDefault, grpcWebContentTypePrefix
	}
	if responseContentType == prefix+requestCodecName ||
		(requestCodecName == encoding.CodecNameProto && responseContentType == bare) {
		return nil
	}
	expectedContentType := bare
	if requestCodecName != encoding.CodecNameProto {
		expectedContentType = prefix + requestCodecName
	}
	code := errors.Internal
	if responseContentType != bare && !strings.HasPrefix(responseContentType, prefix) {
		// Doesn't even look like a gRPC response? Use code "unknown".
		code = errors.Unknown
	}
	return errors.Newf(
		"invalid content-type: %q; expecting %q",
		responseContentType,
		expectedContentType,
	).WithCode(code)
}
