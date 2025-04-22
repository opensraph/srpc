package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/mem"
)

// stableCodec is an extension to Codec for serializing with stable output.
type stableCodec interface {
	encoding.Codec

	// MarshalStable marshals the given message with stable field ordering.
	//
	// MarshalStable should return the same output for a given input. Although
	// it is not guaranteed to be canonicalized, the marshalling routine for
	// MarshalStable will opt for the most normalized output available for a
	// given serialization.
	//
	// For practical reasons, it is possible for MarshalStable to return two
	// different results for two inputs considered to be "equal" in their own
	// domain, and it may change in the future with codec updates, but for
	// any given concrete value and any given version, it should return the
	// same output.
	MarshalStable(any) (mem.BufferSlice, error)

	// IsBinary returns true if the marshalled data is binary for this codec.
	//
	// If this function returns false, the data returned from Marshal and
	// MarshalStable are considered valid text and may be used in contexts
	// where text is expected.
	IsBinary() bool
}

type connectStreamingMarshaler struct {
	envelope.EnvelopeWriter
}

func (m *connectStreamingMarshaler) MarshalEndStream(err error, trailer http.Header) error {
	end := &connectEndStreamMessage{Trailer: trailer}
	if err != nil {
		end.Error = newConnectWireError(err)
		if srpcErr, ok := errors.AsError(err); ok && !srpcErr.IsWireError() {
			headers.MergeNonProtocolHeaders(end.Trailer, srpcErr.Meta())
		}
	}
	data, marshalErr := json.Marshal(end)
	if marshalErr != nil {
		return errors.Newf("marshal end stream: %w", marshalErr).WithCode(errors.Internal)
	}
	raw := mem.BufferSlice{mem.SliceBuffer(data)}
	return m.Write(&envelope.Envelope{
		Data:  raw,
		Flags: connectFlagEnvelopeEndStream,
	})
}

type connectStreamingUnmarshaler struct {
	envelope.EnvelopeReader

	endStreamErr *errors.Error
	trailer      http.Header
}

func (u *connectStreamingUnmarshaler) Unmarshal(message any) error {
	err := u.EnvelopeReader.Unmarshal(message)
	if err == nil {
		return nil
	}
	if !errors.Is(err, envelope.ErrSpecialEnvelope) {
		return err
	}
	env := u.Last
	data := env.Data
	u.Last.Data = nil // don't keep a reference to it
	if !env.IsSet(connectFlagEnvelopeEndStream) {
		return errors.Newf("protocol error: invalid envelope flags %d", env.Flags).WithCode(errors.Internal)
	}
	var end connectEndStreamMessage
	b := data.Materialize()

	if err := json.Unmarshal(b, &end); err != nil {
		return errors.Newf("unmarshal end stream message: %w", err).WithCode(errors.Internal)
	}
	for name, value := range end.Trailer {
		canonical := http.CanonicalHeaderKey(name)
		if name != canonical {
			headers.DelHeaderCanonical(end.Trailer, name)
			end.Trailer[canonical] = append(end.Trailer[canonical], value...)
		}
	}
	u.trailer = end.Trailer
	u.endStreamErr = end.Error.asError()
	return envelope.ErrSpecialEnvelope
}

func (u *connectStreamingUnmarshaler) Trailer() http.Header {
	return u.trailer
}

func (u *connectStreamingUnmarshaler) EndStreamError() *errors.Error {
	return u.endStreamErr
}

type connectUnaryMarshaler struct {
	ctx              context.Context //nolint:containedctx
	sender           duplex.MessageSender
	codec            encoding.Codec
	compressMinBytes int
	compressionName  string
	compressionPool  *compress.CompressionPool
	bufferPool       mem.BufferPool
	header           http.Header
	sendMaxBytes     int
	wroteHeader      bool
}

func (m *connectUnaryMarshaler) Marshal(message any) error {
	if message == nil {
		return m.write(nil)
	}

	data, err := m.codec.Marshal(message)
	defer data.Free()
	if err != nil {
		return errors.Newf("marshal message: %w", err).WithCode(errors.Internal)
	}
	if data.Len() > 0 && m.compressionPool != nil {
		compressed, isCompressed, err := compress.Compress(data, m.compressionPool, m.bufferPool)
		if err != nil {
			return errors.Newf("compress message: %w", err).WithCode(errors.Internal)
		}
		if isCompressed {
			data = compressed
			compressed.Free()
		}
	}

	if m.sendMaxBytes > 0 && data.Len() > m.sendMaxBytes {
		return errors.Newf("message size %d exceeds sendMaxBytes %d", data.Len(), m.sendMaxBytes).WithCode(errors.ResourceExhausted)
	}

	headers.SetHeaderCanonical(m.header, connectUnaryHeaderCompression, m.compressionName)
	return m.write(data)
}

func (m *connectUnaryMarshaler) write(data mem.BufferSlice) error {
	m.wroteHeader = true
	payload := bytes.NewReader(data.Materialize())
	defer data.Free()
	if _, err := m.sender.Send(payload); err != nil {
		err = errors.FromContextError(err)
		if ok := errors.As(err, new(*errors.Error)); ok {
			return err
		}
		return errors.Newf("write message: %w", err).WithCode(errors.Unknown)
	}
	return nil
}

type connectUnaryRequestMarshaler struct {
	connectUnaryMarshaler

	enableGet      bool
	getURLMaxBytes int
	getUseFallback bool
	stableCodec    stableCodec
	duplexCall     *duplex.DuplexHTTPCall
}

func (m *connectUnaryRequestMarshaler) Marshal(message any) error {
	if m.enableGet {
		if m.stableCodec == nil && !m.getUseFallback {
			return errors.Newf("codec %s doesn't support stable marshal; can't use get", m.codec.Name()).WithCode(errors.Internal)
		}
		if m.stableCodec != nil {
			return m.marshalWithGet(message)
		}
	}
	return m.connectUnaryMarshaler.Marshal(message)
}

func (m *connectUnaryRequestMarshaler) marshalWithGet(message any) error {
	var data mem.BufferSlice
	defer data.Free()
	var err error
	if message != nil {
		data, err = m.stableCodec.MarshalStable(message)
		if err != nil {
			return errors.Newf("marshal message stable: %w", err).WithCode(errors.Internal)
		}
	}
	if m.compressionPool != nil {
		compressed, isCompressed, err := compress.Compress(data, m.compressionPool, m.bufferPool)
		if err != nil {
			return errors.Newf("compress message: %w", err).WithCode(errors.Internal)
		}
		if isCompressed {
			data = compressed
			compressed.Free()
		}

	}

	isTooBig := m.sendMaxBytes > 0 && data.Len() > m.sendMaxBytes
	if isTooBig {
		return errors.Newf("message size %d exceeds sendMaxBytes %d", data.Len(), m.sendMaxBytes).WithCode(errors.ResourceExhausted)
	}

	url := m.buildGetURL(data.Materialize(), false)
	if m.getURLMaxBytes <= 0 || len(url.String()) < m.getURLMaxBytes {
		m.writeWithGet(url)
		return nil
	}

	return errors.Newf("url size %d exceeds getURLMaxBytes %d", len(url.String()), m.getURLMaxBytes).WithCode(errors.ResourceExhausted)
}

func (m *connectUnaryRequestMarshaler) buildGetURL(data []byte, compressed bool) *url.URL {
	url := *m.duplexCall.URL()
	query := url.Query()
	query.Set(connectUnaryConnectQueryParameter, connectUnaryConnectQueryValue)
	query.Set(connectUnaryEncodingQueryParameter, m.codec.Name())
	if m.stableCodec.IsBinary() || compressed {
		query.Set(connectUnaryMessageQueryParameter, encodeBinaryQueryValue(data))
		query.Set(connectUnaryBase64QueryParameter, "1")
	} else {
		query.Set(connectUnaryMessageQueryParameter, string(data))
	}
	if compressed {
		query.Set(connectUnaryCompressionQueryParameter, m.compressionName)
	}
	url.RawQuery = query.Encode()
	return &url
}

func (m *connectUnaryRequestMarshaler) writeWithGet(url *url.URL) {
	headers.DelHeaderCanonical(m.header, connectHeaderProtocolVersion)
	headers.DelHeaderCanonical(m.header, headers.HeaderContentType)
	headers.DelHeaderCanonical(m.header, headers.HeaderContentEncoding)
	headers.DelHeaderCanonical(m.header, headers.HeaderContentLength)
	m.duplexCall.SetMethod(http.MethodGet)
	*m.duplexCall.URL() = *url
}

type connectUnaryUnmarshaler struct {
	ctx             context.Context //nolint:containedctx
	reader          io.Reader
	codec           encoding.Codec
	compressionName string
	compressionPool *compress.CompressionPool
	bufferPool      mem.BufferPool
	alreadyRead     bool
	readMaxBytes    int
}

func (u *connectUnaryUnmarshaler) Unmarshal(message any) error {
	return u.UnmarshalFunc(message, u.codec.Unmarshal)
}

func (u *connectUnaryUnmarshaler) UnmarshalFunc(message any, unmarshal func(mem.BufferSlice, any) error) error {
	if u.alreadyRead {
		return errors.FromError(io.EOF).WithCode(errors.Internal)
	}
	u.alreadyRead = true
	reader := u.reader
	if u.readMaxBytes > 0 && int64(u.readMaxBytes) < math.MaxInt64 {
		reader = io.LimitReader(u.reader, int64(u.readMaxBytes)+1)
	}
	// ReadFrom ignores io.EOF, so any error here is real.
	var data mem.BufferSlice
	defer data.Free()
	w := mem.NewWriter(&data, u.bufferPool)
	bytesRead, err := io.Copy(w, reader)
	if err != nil {
		err = errors.WrapIfMaxBytesError(err, "read first %d bytes of message", bytesRead)
		err = errors.WrapIfContextDone(u.ctx, err)
		if ok := errors.As(err, new(*errors.Error)); ok {
			return err
		}
		return errors.Newf("read message: %w", err).WithCode(errors.Unknown)
	}
	if u.readMaxBytes > 0 && bytesRead > int64(u.readMaxBytes) {
		// Attempt to read to end in order to allow connection re-use
		discardedBytes, err := io.Copy(io.Discard, u.reader)
		if err != nil {
			return errors.Newf("message is larger than configured max %d - unable to determine message size: %w", u.readMaxBytes, err).WithCode(errors.ResourceExhausted)
		}
		return errors.Newf("message size %d is larger than configured max %d", bytesRead+discardedBytes, u.readMaxBytes).WithCode(errors.ResourceExhausted)
	}
	if data.Len() > 0 && u.compressionPool != nil {
		decompressed, isDecompressed, err := compress.Decompress(data, u.readMaxBytes, u.compressionPool, u.bufferPool)
		if err != nil {
			return errors.Newf("decompress message: %w", err).WithCode(errors.Internal)
		}
		if isDecompressed {
			data = decompressed
			decompressed.Free()
		}
	}
	if err := unmarshal(data, message); err != nil {
		return errors.Newf("unmarshal message: %w", err).WithCode(errors.InvalidArgument)
	}
	return nil
}
