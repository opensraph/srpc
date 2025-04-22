package grpc

import (
	"bufio"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/mem"
)

type grpcMarshaler struct {
	envelope.EnvelopeWriter
}

func (m *grpcMarshaler) MarshalWebTrailers(trailer http.Header) error {
	var bs mem.BufferSlice
	w := mem.NewWriter(&bs, m.EnvelopeWriter.BufferPool)
	for key, values := range trailer {
		// Per the Go specification, keys inserted during iteration may be produced
		// later in the iteration or may be skipped. For safety, avoid mutating the
		// map if the key is already lower-cased.
		lower := strings.ToLower(key)
		if key == lower {
			continue
		}
		delete(trailer, key)
		trailer[lower] = values
	}
	if err := trailer.Write(w); err != nil {
		return errors.Newf("marshal web trailers: %w", err).WithCode(errors.Internal)
	}
	return m.Write(&envelope.Envelope{
		Data:  bs,
		Flags: grpcFlagEnvelopeTrailer,
	})
}

type grpcUnmarshaler struct {
	envelope.EnvelopeReader

	web        bool
	webTrailer http.Header
}

func (u *grpcUnmarshaler) Unmarshal(message any) error {
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
	defer data.Free()
	if !u.web || !env.IsSet(grpcFlagEnvelopeTrailer) {
		return errors.Newf(
			"protocol error: invalid envelope flags %d", env.Flags,
		).WithCode(errors.Internal)
	}

	// Per the gRPC-Web specification, trailers should be encoded as an HTTP/1
	// headers block _without_ the terminating newline. To make the headers
	// parseable by net/textproto, we need to add the newline.
	w := mem.NewWriter(&data, u.EnvelopeReader.BufferPool)
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return errors.Newf(
			"unmarshal web trailers: %w", err,
		).WithCode(errors.Internal)
	}
	bufferedReader := bufio.NewReader(data.Reader())
	mimeReader := textproto.NewReader(bufferedReader)
	mimeHeader, mimeErr := mimeReader.ReadMIMEHeader()
	if mimeErr != nil {
		return errors.Newf(
			"unmarshal web trailers: %w", mimeErr,
		).WithCode(errors.Internal)
	}
	u.webTrailer = http.Header(mimeHeader)
	return envelope.ErrSpecialEnvelope
}

func (u *grpcUnmarshaler) WebTrailer() http.Header {
	return u.webTrailer
}
