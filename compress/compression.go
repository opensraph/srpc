package compress

import (
	"io"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/mem"
)

const (
	CompressionGzip     = "gzip"
	CompressionIdentity = "identity"
)

// A Decompressor is a reusable wrapper that decompresses an underlying data
// source. The standard library's [*gzip.Reader] implements Decompressor.
type Decompressor interface {
	io.Reader

	// Close closes the Decompressor, but not the underlying data source. It may
	// return an error if the Decompressor wasn't read to EOF.
	Close() error

	// Reset discards the Decompressor's internal state, if any, and prepares it
	// to read from a new source of compressed data.
	Reset(io.Reader) error
}

// A Compressor is a reusable wrapper that compresses data written to an
// underlying sink. The standard library's [*gzip.Writer] implements Compressor.
type Compressor interface {
	io.Writer

	// Close flushes any buffered data to the underlying sink, then closes the
	// Compressor. It must not close the underlying sink.
	Close() error

	// Reset discards the Compressor's internal state, if any, and prepares it to
	// write compressed data to a new sink.
	Reset(io.Writer)
}

type CompressionPool struct {
	bufferPool    mem.BufferPool
	decompressors sync.Pool
	compressors   sync.Pool
}

func NewCompressionPool(compressor Compressor, decompressor Decompressor, bufferPool mem.BufferPool) *CompressionPool {
	if decompressor == nil || compressor == nil {
		return nil
	}
	return &CompressionPool{
		bufferPool: bufferPool,
		decompressors: sync.Pool{
			New: func() any { return decompressor },
		},
		compressors: sync.Pool{
			New: func() any { return compressor },
		},
	}
}

func (c *CompressionPool) Decompress(src mem.BufferSlice, readMaxBytes int64) (mem.BufferSlice, error) {
	decompressor, err := c.getDecompressor(src.Reader())
	if err != nil && err != io.EOF {
		return nil, errors.Newf("get decompressor: %w", err).WithCode(errors.Unknown)
	}
	reader := io.Reader(decompressor)
	if readMaxBytes > 0 && readMaxBytes < math.MaxInt64 {
		reader = io.LimitReader(decompressor, readMaxBytes+1)
	}

	var dst mem.BufferSlice
	w := mem.NewWriter(&dst, c.bufferPool)
	wrapErr := func(err error) error {
		dst.Free()
		err = errors.FromContextError(err)
		if ok := errors.As(err, new(*errors.Error)); ok {
			return err
		}
		return errors.Newf("error while decompress: %w", err).WithCode(errors.Internal)
	}
	written, err := io.Copy(w, reader)
	if err != nil {
		return nil, wrapErr(err)
	}

	if readMaxBytes > 0 && written > readMaxBytes {
		_ = c.putDecompressor(decompressor)
		return nil, wrapErr(errors.Newf(
			"message size %d is larger than configured max %d",
			written, readMaxBytes,
		).WithCode(errors.ResourceExhausted))
	}

	if err := c.putDecompressor(decompressor); err != nil {
		return nil, wrapErr(errors.Newf(
			"recycle decompressor: %w", err,
		).WithCode(errors.Unknown))
	}
	return dst, nil
}

func (c *CompressionPool) Compress(src mem.BufferSlice) (mem.BufferSlice, error) {
	var dst mem.BufferSlice
	writer := mem.NewWriter(&dst, c.bufferPool)

	wrapErr := func(err error) error {
		dst.Free()
		err = errors.FromContextError(err)
		if ok := errors.As(err, new(*errors.Error)); ok {
			return err
		}
		return errors.Newf("error while compress: %w", err).WithCode(errors.Internal)
	}

	compressor, err := c.getCompressor(writer)
	if err != nil {
		return nil, wrapErr(errors.Newf("get compressor: %w", err).WithCode(errors.Unknown))
	}
	if _, err := io.Copy(compressor, src.Reader()); err != nil {
		_ = c.putCompressor(compressor)
		return nil, wrapErr(errors.Newf("compress: %w", err).WithCode(errors.Internal))
	}
	if err := c.putCompressor(compressor); err != nil {
		return nil, wrapErr(errors.Newf("recycle compressor: %w", err).WithCode(errors.Internal))
	}
	return dst, nil
}

func (c *CompressionPool) getDecompressor(reader io.Reader) (Decompressor, error) {
	decompressor, ok := c.decompressors.Get().(Decompressor)
	if !ok {
		return nil, errors.New("expected Decompressor, got incorrect type from pool")
	}
	return decompressor, decompressor.Reset(reader)
}

func (c *CompressionPool) putDecompressor(decompressor Decompressor) error {
	if err := decompressor.Close(); err != nil {
		return err
	}
	// While it's in the pool, we don't want the decompressor to retain a
	// reference to the underlying reader. However, most decompressors attempt to
	// read some header data from the new data source when Reset; since we don't
	// know the compression format, we can't provide a valid header. Since we
	// also reset the decompressor when it's pulled out of the pool, we can
	// ignore errors here.
	_ = decompressor.Reset(http.NoBody)
	c.decompressors.Put(decompressor)
	return nil
}

func (c *CompressionPool) getCompressor(writer io.Writer) (Compressor, error) {
	compressor, ok := c.compressors.Get().(Compressor)
	if !ok {
		return nil, errors.New("expected Compressor, got incorrect type from pool")
	}
	compressor.Reset(writer)
	return compressor, nil
}

func (c *CompressionPool) putCompressor(compressor Compressor) error {
	if err := compressor.Close(); err != nil {
		return err
	}
	compressor.Reset(io.Discard) // don't keep references
	c.compressors.Put(compressor)
	return nil
}

// NegotiateCompression determines and validates the request compression and
// response compression using the available compressors and protocol-specific
// Content-Encoding and Accept-Encoding headers.
func NegotiateCompression( //nolint:nonamedreturns
	availableCompressors ReadOnlyCompressionPools,
	sent, accept string,
) (requestCompression, responseCompression string, clientVisibleErr error) {
	requestCompression = CompressionIdentity
	if sent != "" && sent != CompressionIdentity {
		// We default to identity, so we only care if the client sends something
		// other than the empty string or compressIdentity.
		if availableCompressors.Contains(sent) {
			requestCompression = sent
		} else {
			// To comply with
			// https://github.com/grpc/grpc/blob/master/doc/compression.md and the
			// Connect protocol, we should return CodeUnimplemented and specify
			// acceptable compression(s) (in addition to setting the a
			// protocol-specific accept-encoding header).
			return "", "", errors.Newf(
				"unknown compression %q: supported encodings are %v",
				sent, availableCompressors.CommaSeparatedNames(),
			).WithCode(errors.Unimplemented)
		}
	}
	// Support asymmetric compression. This logic follows
	// https://github.com/grpc/grpc/blob/master/doc/compression.md and common
	// sense.
	responseCompression = requestCompression
	// If we're not already planning to compress the response, check whether the
	// client requested a compression algorithm we support.
	if responseCompression == CompressionIdentity && accept != "" {
		for _, name := range strings.FieldsFunc(accept, isCommaOrSpace) {
			if availableCompressors.Contains(name) {
				// We found a mutually supported compression algorithm. Unlike standard
				// HTTP, there's no preference weighting, so can bail out immediately.
				responseCompression = name
				break
			}
		}
	}
	return requestCompression, responseCompression, nil
}

// ReadOnlyCompressionPools is a read-only interface to a map of named
// CompressionPools.
type ReadOnlyCompressionPools interface {
	Get(string) *CompressionPool
	Contains(string) bool
	// Wordy, but clarifies how this is different from readOnlyCodecs.Names().
	CommaSeparatedNames() string
}

func NewReadOnlyCompressionPools(
	nameToPool map[string]*CompressionPool,
	reversedNames []string,
) ReadOnlyCompressionPools {
	// Client and handler configs keep compression names in registration order,
	// but we want the last registered to be the most preferred.
	names := make([]string, 0, len(reversedNames))
	seen := make(map[string]struct{}, len(reversedNames))
	for i := len(reversedNames) - 1; i >= 0; i-- {
		name := reversedNames[i]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return &namedCompressionPools{
		nameToPool:          nameToPool,
		commaSeparatedNames: strings.Join(names, ","),
	}
}

var _ ReadOnlyCompressionPools = (*namedCompressionPools)(nil)

type namedCompressionPools struct {
	nameToPool          map[string]*CompressionPool
	commaSeparatedNames string
}

func (m *namedCompressionPools) Get(name string) *CompressionPool {
	if name == "" || name == CompressionIdentity {
		return nil
	}
	return m.nameToPool[name]
}

func (m *namedCompressionPools) Contains(name string) bool {
	_, ok := m.nameToPool[name]
	return ok
}

func (m *namedCompressionPools) CommaSeparatedNames() string {
	return m.commaSeparatedNames
}

func isCommaOrSpace(c rune) bool {
	return c == ',' || c == ' '
}
