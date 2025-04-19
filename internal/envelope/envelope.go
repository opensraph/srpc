package envelope

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/internal/utils"
	"github.com/opensraph/srpc/mem"
)

// Constants for envelope prefix length and flags
const (
	envelopePrefixLength   = 5
	flagEnvelopeCompressed = 0b00000001
)

var ErrSpecialEnvelope = errors.Newf(
	"final message has protocol-specific flags: %w",
	io.EOF,
).WithCode(errors.Unknown)

// Envelope represents a block of arbitrary bytes wrapped in gRPC and Connect's framing protocol.
type Envelope struct {
	Data   mem.BufferSlice
	Flags  uint8
	offset int64
}

var _ duplex.MessagePayload = (*Envelope)(nil)

// Check if a flag is set in the envelope
func (e *Envelope) IsSet(flag uint8) bool {
	return (e.Flags & flag) != 0
}

// Read implements [io.Reader].
func (e *Envelope) Read(data []byte) (int, error) {
	if e.offset < envelopePrefixLength {
		prefix, err := makeEnvelopePrefix(e.Flags, e.Data.Len())
		if err != nil {
			return 0, fmt.Errorf("create envelope prefix: %w", err)
		}
		readN := copy(data, prefix[e.offset:])
		e.offset += int64(readN)
		if e.offset < envelopePrefixLength {
			return readN, nil
		}
		data = data[readN:]
	}
	if remainingData := e.Data.Materialize(); len(remainingData) > 0 {
		n := copy(data, remainingData[e.offset-envelopePrefixLength:])
		e.offset += int64(n)
		return handleEOF(e.offset, int64(len(remainingData)+envelopePrefixLength))
	}
	return 0, io.EOF
}

// WriteTo implements [io.WriterTo].
func (e *Envelope) WriteTo(dst io.Writer) (wroteN int64, err error) {
	if e.offset < 5 {
		prefix, err := makeEnvelopePrefix(e.Flags, e.Data.Len())
		if err != nil {
			return 0, err
		}
		prefixN, err := dst.Write(prefix[e.offset:])
		e.offset += int64(prefixN)
		wroteN += int64(prefixN)
		if e.offset < 5 {
			return wroteN, err
		}
	}
	n, err := dst.Write(e.Data.Materialize()[e.offset-5:])
	e.offset += int64(n)
	wroteN += int64(n)
	return wroteN, err
}

// Seek implements [io.Seeker].
func (e *Envelope) Seek(offset int64, whence int) (int64, error) {
	abs, err := calculateOffset(e.offset, offset, whence, e.Data.Len())
	if err != nil {
		return 0, err
	}
	e.offset = abs
	return abs, nil
}

// Helper to calculate seek offset
func calculateOffset(current, offset int64, whence int, dataLen int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = current + offset
	case io.SeekEnd:
		abs = int64(dataLen) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	return abs, nil
}

// Len returns the number of bytes of the unread portion of the envelope.
func (e *Envelope) Len() int {
	remaining := int(int64(e.Data.Len()) + envelopePrefixLength - e.offset)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// EnvelopeWriter is responsible for writing Envelope messages.
type EnvelopeWriter struct {
	Ctx              context.Context
	Sender           duplex.MessageSender
	Codec            encoding.Codec
	CompressMinBytes int
	CompressionPool  *compress.CompressionPool
	BufferPool       mem.BufferPool
	SendMaxBytes     int
}

// Marshal serializes a message and writes it as an Envelope.
func (w *EnvelopeWriter) Marshal(message any) error {
	marshaledBytes, err := w.Codec.Marshal(message)
	if err != nil {
		return errors.Newf("marshal message: %w", err).WithCode(errors.Internal)
	}
	envelope := &Envelope{Data: marshaledBytes}
	return w.Write(envelope)
}

// Write writes the enveloped message, compressing if necessary.
func (w *EnvelopeWriter) Write(env *Envelope) error {
	if env.IsSet(flagEnvelopeCompressed) ||
		w.CompressionPool == nil ||
		env.Data.Len() < w.CompressMinBytes {
		if w.SendMaxBytes > 0 && env.Data.Len() > w.SendMaxBytes {
			return errors.Newf(
				"message size %d exceeds sendMaxBytes %d", env.Data.Len(), w.SendMaxBytes,
			).WithCode(errors.ResourceExhausted)
		}
		return w.write(env)
	}
	data, err := w.CompressionPool.Compress(env.Data)
	if err != nil {
		return err
	}
	if w.SendMaxBytes > 0 && data.Len() > w.SendMaxBytes {
		return errors.Newf(
			"compressed message size %d exceeds sendMaxBytes %d", data.Len(), w.SendMaxBytes,
		).WithCode(errors.ResourceExhausted)
	}
	return w.write(&Envelope{
		Data:  data,
		Flags: env.Flags | flagEnvelopeCompressed,
	})
}

// Helper to write envelope
func (w *EnvelopeWriter) write(env *Envelope) error {
	if _, err := w.Sender.Send(env); err != nil {
		return errors.Newf("write envelope: %w", err).WithCode(errors.Unknown)
	}
	return nil
}

var _ duplex.MessageSender = WriteSender{}

// WriteSender is a sender that writes to an [io.Writer]. Useful for wrapping
// [http.ResponseWriter].
type WriteSender struct {
	Writer io.Writer
}

func (w WriteSender) Send(payload duplex.MessagePayload) (int64, error) {
	return payload.WriteTo(w.Writer)
}

// EnvelopeReader is responsible for reading Envelope messages.
type EnvelopeReader struct {
	Ctx             context.Context
	Last            Envelope
	BufferPool      mem.BufferPool
	Reader          io.Reader
	BytesRead       int64
	Codec           encoding.Codec
	CompressionPool *compress.CompressionPool
	ReadMaxBytes    int
}

// Unmarshal reads an Envelope, decompresses its data if necessary, and unmarshal it.
func (r *EnvelopeReader) Unmarshal(message any) error {
	var buffer mem.BufferSlice
	env := &Envelope{Data: buffer}
	err := r.Read(env)
	switch {
	case err == nil && env.IsSet(flagEnvelopeCompressed) && r.CompressionPool == nil:
		return errors.New(
			"protocol error: sent compressed message without compression support",
		).WithCode(errors.Internal)
	case err == nil &&
		(env.Flags == 0 || env.Flags == flagEnvelopeCompressed) &&
		env.Data.Len() == 0:
		// This is a standard message (because none of the top 7 bits are set) and
		// there's no data, so the zero value of the message is correct.
		return nil
	case err != nil && errors.Is(err, io.EOF):
		// The stream has ended. Propagate the EOF to the caller.
		return err
	case err != nil:
		// Something's wrong.
		return err
	}

	data := env.Data
	if data.Len() > 0 && env.IsSet(flagEnvelopeCompressed) {
		decompressed, err := r.CompressionPool.Decompress(data, int64(r.ReadMaxBytes))
		defer decompressed.Free()
		if err != nil {
			return err
		}
		data = decompressed
	}

	if env.Flags != 0 && env.Flags != flagEnvelopeCompressed {
		// Drain the rest of the stream to ensure there is no extra data.
		numBytes, err := utils.Discard(r.Reader)
		r.BytesRead += numBytes
		if err != nil {
			err = errors.FromContextError(err)
			if connErr, ok := errors.AsError(err); ok {
				return connErr
			}
			return errors.Newf(
				"corrupt response: I/O error after end-stream message: %w", err,
			).WithCode(errors.Internal)
		} else if numBytes > 0 {
			return errors.Newf(
				"corrupt response: %d extra bytes after end of stream", numBytes,
			).WithCode(errors.Internal)
		}
		// One of the protocol-specific flags are set, so this is the end of the
		// stream. Save the message for protocol-specific code to process and
		// return a sentinel error. We alias the buffer with dontRelease as a
		// way of marking it so above defers don't release it to the pool.
		r.Last = Envelope{
			Data:  data,
			Flags: env.Flags,
		}
		return ErrSpecialEnvelope
	}

	if err := r.Codec.Unmarshal(data, message); err != nil {
		return errors.Newf("unmarshal message: %w", err).WithCode(errors.InvalidArgument)
	}
	return nil
}

// Read reads an Envelope from the underlying reader.
func (r *EnvelopeReader) Read(env *Envelope) error {
	prefixes := [envelopePrefixLength]byte{}
	n, err := io.ReadFull(r.Reader, prefixes[:])
	r.BytesRead += int64(n)
	if err != nil {
		return handleReadError(err)
	}
	size := int64(binary.BigEndian.Uint32(prefixes[1:5]))
	if r.ReadMaxBytes > 0 && size > int64(r.ReadMaxBytes) {
		return handleOversizeMessage(r, size)
	}
	w := mem.NewWriter(&env.Data, r.BufferPool)
	readN, err := io.CopyN(w, r.Reader, size)
	r.BytesRead += readN
	if err != nil {
		return handleIncompleteMessage(err, size, readN)
	}
	env.Flags = prefixes[0]
	return nil
}

// Helper to handle EOF edge cases
func handleEOF(offset, total int64) (int, error) {
	if offset >= total {
		return 0, io.EOF
	}
	return 0, nil
}

// Helper to generate envelope prefix
func makeEnvelopePrefix(flags uint8, size int) ([5]byte, error) {
	if size < 0 || size > math.MaxUint32 {
		return [5]byte{}, fmt.Errorf("size %d out of bounds", size)
	}
	return [5]byte{
		flags,
		byte(size >> 24),
		byte(size >> 16),
		byte(size >> 8),
		byte(size),
	}, nil
}

// Helper to handle read errors
func handleReadError(err error) error {
	if errors.Is(err, io.EOF) {
		return errors.FromError(err).WithCode(errors.Unknown)
	}
	return errors.Newf("protocol error: incomplete envelope: %w", err).WithCode(errors.InvalidArgument)
}

// Helper to handle oversized messages
func handleOversizeMessage(r *EnvelopeReader, size int64) error {
	io.CopyN(io.Discard, r.Reader, size)
	return errors.Newf("message size %d exceeds max %d", size, r.ReadMaxBytes).WithCode(errors.ResourceExhausted)
}

// Helper to handle incomplete messages
func handleIncompleteMessage(err error, size, readN int64) error {
	if errors.Is(err, io.EOF) {
		return errors.Newf("promised %d bytes, got %d bytes", size, readN).WithCode(errors.InvalidArgument)
	}
	return errors.Newf("read envelope message: %w", err).WithCode(errors.Unknown)
}
