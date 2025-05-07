package compress

import (
	"io"
	"math"
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
	poolCompressor   sync.Pool
	poolDecompressor sync.Pool
}

func NewCompressionPool(compressor Compressor, decompressor Decompressor) *CompressionPool {
	return &CompressionPool{
		poolCompressor: sync.Pool{
			New: func() any {
				return compressor
			},
		},
		poolDecompressor: sync.Pool{
			New: func() any {
				return decompressor
			},
		},
	}
}

type reader struct {
	Decompressor
	pool *sync.Pool
}

func (c *CompressionPool) Decompress(r io.Reader) (io.Reader, error) {
	z, inPool := c.poolDecompressor.Get().(Decompressor)
	if !inPool {
		return nil, errors.Newf("failed to get decompressor from pool")
	}
	if err := z.Reset(r); err != nil {
		c.poolDecompressor.Put(z)
		return nil, err
	}
	return z, nil
}

type writer struct {
	Compressor
	pool *sync.Pool
}

func (c *CompressionPool) Compress(w io.Writer) (io.WriteCloser, error) {
	z, inPool := c.poolCompressor.Get().(Compressor)
	if !inPool {
		return nil, errors.Newf("failed to get compressor from pool")
	}
	z.Reset(w)
	return z, nil
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

func Compress(in mem.BufferSlice, compressor *CompressionPool, pool mem.BufferPool) (out mem.BufferSlice, isCompressed bool, err error) {
	if compressor == nil || in.Len() == 0 {
		return nil, false, nil
	}
	w := mem.NewWriter(&out, pool)
	wrapErr := func(err error) error {
		out.Free()
		return errors.Newf("srpc: error while compressing: %v", err.Error()).WithCode(errors.Internal)
	}
	z, err := compressor.Compress(w)
	if err != nil {
		return nil, false, wrapErr(err)
	}
	for _, b := range in {
		if _, err := z.Write(b.ReadOnlyData()); err != nil {
			return nil, false, wrapErr(err)
		}
	}
	if err := z.Close(); err != nil {
		return nil, false, wrapErr(err)
	}
	return out, true, nil
}

func Decompress(d mem.BufferSlice, maxReceiveMessageSize int, decompressor *CompressionPool, pool mem.BufferPool) (out mem.BufferSlice, isDecompressed bool, err error) {
	if decompressor == nil || d.Len() == 0 {
		return nil, false, nil
	}
	dcReader, err := decompressor.Decompress(d.Reader())
	if err != nil {
		return nil, false, errors.Newf("srpc: failed to decompress the message: %v", err).WithCode(errors.Internal)
	}

	// Read at most one byte more than the limit from the decompressor.
	// Unless the limit is MaxInt64, in which case, that's impossible, so
	// apply no limit.
	if limit := int64(maxReceiveMessageSize); limit < math.MaxInt64 {
		dcReader = io.LimitReader(dcReader, limit+1)
	}
	out, err = mem.ReadAll(dcReader, pool)
	if err != nil {
		out.Free()
		return nil, false, errors.Newf("srpc: failed to read decompressed data: %v", err).WithCode(errors.Internal)
	}

	if out.Len() > maxReceiveMessageSize {
		out.Free()
		return nil, false, errors.Newf("srpc: received message after decompression larger than max %d", maxReceiveMessageSize).WithCode(errors.ResourceExhausted)
	}
	return out, true, nil
}
