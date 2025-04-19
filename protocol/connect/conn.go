package connect

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/mem"
	"github.com/opensraph/srpc/protocol"
)

var _ protocol.StreamingClientConn = (*connectUnaryClientConn)(nil)

type connectUnaryClientConn struct {
	spec             protocol.Spec
	peer             protocol.Peer
	duplexCall       *duplex.DuplexHTTPCall
	compressionPools compress.ReadOnlyCompressionPools
	bufferPool       mem.BufferPool
	marshaler        connectUnaryRequestMarshaler
	unmarshaler      connectUnaryUnmarshaler
	responseHeader   http.Header
	responseTrailer  http.Header
}

func (cc *connectUnaryClientConn) Spec() protocol.Spec {
	return cc.spec
}

func (cc *connectUnaryClientConn) Peer() protocol.Peer {
	return cc.peer
}

func (cc *connectUnaryClientConn) Send(msg any) error {
	if err := cc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (cc *connectUnaryClientConn) RequestHeader() http.Header {
	return cc.duplexCall.Header()
}

func (cc *connectUnaryClientConn) CloseRequest() error {
	return cc.duplexCall.CloseWrite()
}

func (cc *connectUnaryClientConn) Receive(msg any) error {
	if err := cc.duplexCall.BlockUntilResponseReady(); err != nil {
		return err
	}
	if err := cc.unmarshaler.Unmarshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (cc *connectUnaryClientConn) ResponseHeader() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseHeader
}

func (cc *connectUnaryClientConn) ResponseTrailer() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseTrailer
}

func (cc *connectUnaryClientConn) CloseResponse() error {
	return cc.duplexCall.CloseRead()
}

func (cc *connectUnaryClientConn) OnRequestSend(fn func(*http.Request)) {
	cc.duplexCall.OnRequestSend = fn
}

func (cc *connectUnaryClientConn) validateResponse(response *http.Response) error {
	for k, v := range response.Header {
		if !strings.HasPrefix(k, connectUnaryTrailerPrefix) {
			cc.responseHeader[k] = v
			continue
		}
		cc.responseTrailer[k[len(connectUnaryTrailerPrefix):]] = v
	}
	if err := connectValidateUnaryResponseContentType(
		cc.marshaler.codec.Name(),
		cc.duplexCall.Method(),
		response.StatusCode,
		response.Status,
		headers.GetHeaderCanonical(response.Header, headers.HeaderContentType),
	); err != nil {
		if errors.IsNotModifiedError(err) {
			if err, ok := err.(*errors.Error); ok {
				// Allow access to response headers for this kind of error.
				// RFC 9110 doesn't allow trailers on 304s, so we only need to include headers.
				err.WithMeta(cc.responseHeader.Clone())
			}
		}
		return err
	}
	compression := headers.GetHeaderCanonical(response.Header, connectUnaryHeaderCompression)
	if compression != "" &&
		compression != compress.CompressionIdentity &&
		!cc.compressionPools.Contains(compression) {
		return errors.Newf(
			"unknown encoding %q: accepted encodings are %v",
			compression,
			cc.compressionPools.CommaSeparatedNames(),
		).WithCode(errors.Internal)
	}
	cc.unmarshaler.compressionPool = cc.compressionPools.Get(compression)
	if response.StatusCode != http.StatusOK {
		unmarshaler := connectUnaryUnmarshaler{
			ctx:             cc.unmarshaler.ctx,
			reader:          response.Body,
			compressionPool: cc.unmarshaler.compressionPool,
			bufferPool:      cc.bufferPool,
		}
		var wireErr connectWireError

		unmarshalFunc := func(data mem.BufferSlice, message any) error {
			return json.Unmarshal(data.Materialize(), message)
		}

		if err := unmarshaler.UnmarshalFunc(&wireErr, unmarshalFunc); err != nil {
			return errors.New(
				response.Status,
			).WithCode(errors.HttpToCode(response.StatusCode))
		}
		if wireErr.Code == 0 {
			// code not set? default to one implied by HTTP status
			wireErr.Code = errors.HttpToCode(response.StatusCode)
		}
		serverErr := wireErr.asError()
		if serverErr == nil {
			return nil
		}

		serverErr.WithMeta(cc.responseHeader.Clone())
		serverErr.WithMeta(cc.responseTrailer.Clone())
		return serverErr
	}
	return nil
}

var _ protocol.StreamingClientConn = (*connectStreamingClientConn)(nil)

type connectStreamingClientConn struct {
	spec             protocol.Spec
	peer             protocol.Peer
	duplexCall       *duplex.DuplexHTTPCall
	compressionPools compress.ReadOnlyCompressionPools
	bufferPool       mem.BufferPool
	codec            encoding.Codec
	marshaler        connectStreamingMarshaler
	unmarshaler      connectStreamingUnmarshaler
	responseHeader   http.Header
	responseTrailer  http.Header
}

func (cc *connectStreamingClientConn) Spec() protocol.Spec {
	return cc.spec
}

func (cc *connectStreamingClientConn) Peer() protocol.Peer {
	return cc.peer
}

func (cc *connectStreamingClientConn) Send(msg any) error {
	if err := cc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (cc *connectStreamingClientConn) RequestHeader() http.Header {
	return cc.duplexCall.Header()
}

func (cc *connectStreamingClientConn) CloseRequest() error {
	return cc.duplexCall.CloseWrite()
}

func (cc *connectStreamingClientConn) Receive(msg any) error {
	if err := cc.duplexCall.BlockUntilResponseReady(); err != nil {
		return err
	}
	err := cc.unmarshaler.Unmarshal(msg)
	if err == nil {
		return nil
	}
	// See if the server sent an explicit error in the end-of-stream message.
	headers.MergeHeaders(cc.responseTrailer, cc.unmarshaler.Trailer())
	if serverErr := cc.unmarshaler.EndStreamError(); serverErr != nil {
		// This is expected from a protocol perspective, but receiving an
		// end-of-stream message means that we're _not_ getting a regular message.
		// For users to realize that the stream has ended, Receive must return an
		// error.
		serverErr.WithMeta(cc.responseHeader.Clone())
		serverErr.WithMeta(cc.responseTrailer.Clone())
		_ = cc.duplexCall.CloseWrite()
		return serverErr
	}
	// If the error is EOF but not from a last message, we want to return
	// io.ErrUnexpectedEOF instead.
	if errors.Is(err, io.EOF) && !errors.Is(err, envelope.ErrSpecialEnvelope) {
		err = errors.Newf("protocol error: %w", io.ErrUnexpectedEOF).WithCode(errors.Internal)
	}
	// There's no error in the trailers, so this was probably an error
	// converting the bytes to a message, an error reading from the network, or
	// just an EOF. We're going to return it to the user, but we also want to
	// close the writer so Send errors out.
	_ = cc.duplexCall.CloseWrite()
	return err
}

func (cc *connectStreamingClientConn) ResponseHeader() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseHeader
}

func (cc *connectStreamingClientConn) ResponseTrailer() http.Header {
	_ = cc.duplexCall.BlockUntilResponseReady()
	return cc.responseTrailer
}

func (cc *connectStreamingClientConn) CloseResponse() error {
	return cc.duplexCall.CloseRead()
}

func (cc *connectStreamingClientConn) OnRequestSend(fn func(*http.Request)) {
	cc.duplexCall.OnRequestSend = fn
}

func (cc *connectStreamingClientConn) validateResponse(response *http.Response) error {
	if response.StatusCode != http.StatusOK {
		return errors.Newf("HTTP status %v", response.Status).WithCode(errors.HttpToCode(response.StatusCode))
	}
	if err := connectValidateStreamResponseContentType(
		cc.codec.Name(),
		cc.spec.StreamType,
		headers.GetHeaderCanonical(response.Header, headers.HeaderContentType),
	); err != nil {
		return err
	}
	compression := headers.GetHeaderCanonical(response.Header, connectStreamingHeaderCompression)
	if compression != "" &&
		compression != compress.CompressionIdentity &&
		!cc.compressionPools.Contains(compression) {
		return errors.Newf(
			"unknown encoding %q: accepted encodings are %v",
			compression,
			cc.compressionPools.CommaSeparatedNames(),
		).WithCode(errors.Internal)
	}
	cc.unmarshaler.CompressionPool = cc.compressionPools.Get(compression)
	headers.MergeHeaders(cc.responseHeader, response.Header)
	return nil
}

var _ protocol.StreamingHandlerConn = (*connectUnaryHandlerConn)(nil)

type connectUnaryHandlerConn struct {
	spec            protocol.Spec
	peer            protocol.Peer
	request         *http.Request
	responseWriter  http.ResponseWriter
	marshaler       connectUnaryMarshaler
	unmarshaler     connectUnaryUnmarshaler
	responseTrailer http.Header
}

func (hc *connectUnaryHandlerConn) Spec() protocol.Spec {
	return hc.spec
}

func (hc *connectUnaryHandlerConn) Peer() protocol.Peer {
	return hc.peer
}

func (hc *connectUnaryHandlerConn) Receive(msg any) error {
	if err := hc.unmarshaler.Unmarshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (hc *connectUnaryHandlerConn) RequestHeader() http.Header {
	return hc.request.Header
}

func (hc *connectUnaryHandlerConn) Send(msg any) error {
	hc.mergeResponseHeader(nil /* error */)
	if err := hc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (hc *connectUnaryHandlerConn) ResponseHeader() http.Header {
	return hc.responseWriter.Header()
}

func (hc *connectUnaryHandlerConn) ResponseTrailer() http.Header {
	return hc.responseTrailer
}

func (hc *connectUnaryHandlerConn) Close(err error) error {
	if !hc.marshaler.wroteHeader {
		hc.mergeResponseHeader(err)
		// If the handler received a GET request and the resource hasn't changed,
		// return a 304.
		if len(hc.peer.Query) > 0 && errors.IsNotModifiedError(err) {
			hc.responseWriter.WriteHeader(http.StatusNotModified)
			return hc.request.Body.Close()
		}
	}
	if err == nil || hc.marshaler.wroteHeader {
		return hc.request.Body.Close()
	}
	// In unary Connect, errors always use application/json.
	headers.SetHeaderCanonical(hc.responseWriter.Header(), headers.HeaderContentType, connectUnaryContentTypeJSON)
	hc.responseWriter.WriteHeader(connectCodeToHTTP(errors.CodeOf(err)))
	data, marshalErr := json.Marshal(newConnectWireError(err))
	if marshalErr != nil {
		_ = hc.request.Body.Close()
		return errors.Newf("marshal error: %w", err).WithCode(errors.Internal)
	}
	if _, writeErr := hc.responseWriter.Write(data); writeErr != nil {
		_ = hc.request.Body.Close()
		return writeErr
	}
	return hc.request.Body.Close()
}

func (hc *connectUnaryHandlerConn) GetHTTPMethod() string {
	return hc.request.Method
}

func (hc *connectUnaryHandlerConn) mergeResponseHeader(err error) {
	header := hc.responseWriter.Header()
	if hc.request.Method == http.MethodGet {
		// The response content varies depending on the compression that the client
		// requested (if any). GETs are potentially cacheable, so we should ensure
		// that the Vary header includes at least Accept-Encoding (and not overwrite any values already set).
		header[headerVary] = append(header[headerVary], connectUnaryHeaderAcceptCompression)
	}
	if err != nil {
		if connectErr, ok := errors.AsError(err); ok && !connectErr.IsWireError() {
			headers.MergeNonProtocolHeaders(header, connectErr.Meta())
		}
	}
	for k, v := range hc.responseTrailer {
		header[connectUnaryTrailerPrefix+k] = v
	}
}

var _ protocol.StreamingHandlerConn = (*connectStreamingHandlerConn)(nil)

type connectStreamingHandlerConn struct {
	spec            protocol.Spec
	peer            protocol.Peer
	request         *http.Request
	responseWriter  http.ResponseWriter
	marshaler       connectStreamingMarshaler
	unmarshaler     connectStreamingUnmarshaler
	responseTrailer http.Header
}

func (hc *connectStreamingHandlerConn) Spec() protocol.Spec {
	return hc.spec
}

func (hc *connectStreamingHandlerConn) Peer() protocol.Peer {
	return hc.peer
}

func (hc *connectStreamingHandlerConn) Receive(msg any) error {
	if err := hc.unmarshaler.Unmarshal(msg); err != nil {
		// Clients may not send end-of-stream metadata, so we don't need to handle
		// errSpecialEnvelope.
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (hc *connectStreamingHandlerConn) RequestHeader() http.Header {
	return hc.request.Header
}

func (hc *connectStreamingHandlerConn) Send(msg any) error {
	defer protocol.FlushResponseWriter(hc.responseWriter)
	if err := hc.marshaler.Marshal(msg); err != nil {
		return err
	}
	return nil // must be a literal nil: nil error is a non-nil error
}

func (hc *connectStreamingHandlerConn) ResponseHeader() http.Header {
	return hc.responseWriter.Header()
}

func (hc *connectStreamingHandlerConn) ResponseTrailer() http.Header {
	return hc.responseTrailer
}

func (hc *connectStreamingHandlerConn) Close(err error) error {
	defer protocol.FlushResponseWriter(hc.responseWriter)
	if err := hc.marshaler.MarshalEndStream(err, hc.responseTrailer); err != nil {
		_ = hc.request.Body.Close()
		return err
	}
	// We don't want to copy unread portions of the body to /dev/null here: if
	// the client hasn't closed the request body, we'll block until the server
	// timeout kicks in. This could happen because the client is malicious, but
	// a well-intentioned client may just not expect the server to be returning
	// an error for a streaming RPC. Better to accept that we can't always reuse
	// TCP connections.
	if err := hc.request.Body.Close(); err != nil {
		if connectErr, ok := errors.AsError(err); ok {
			return connectErr
		}
		return errors.FromError(err).WithCode(errors.Unknown)
	}
	return nil // must be a literal nil: nil error is a non-nil error
}
