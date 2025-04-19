package srpc

import (
	"compress/gzip"
	"io"
	"net/http"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/internal/stats"
	"github.com/opensraph/srpc/mem"

	_ "github.com/opensraph/srpc/protocol/connect" // register connect protocol
	_ "github.com/opensraph/srpc/protocol/grpc"    // register grpc protocol

	_ "github.com/opensraph/srpc/encoding/protobinary" // register protobuf codec
	_ "github.com/opensraph/srpc/encoding/protojson"   // register json codec
)

type ServerOption func(o *serverOptions)

type serverOptions struct {
	readTimeout  time.Duration
	writeTimeout time.Duration
	idleTimeout  time.Duration

	readMaxBytes int
	sendMaxBytes int

	bufferPool mem.BufferPool

	statsHandlers []stats.EventHandler

	interceptor Interceptor

	unknownHandler http.Handler

	compressionNames []string
	compressionPools map[string]*compress.CompressionPool
	compressMinBytes int

	codecs encoding.ReadOnlyCodecs
}

var defaultServerOptions = serverOptions{
	bufferPool: mem.DefaultBufferPool(),
}

var globalServerOptions []ServerOption = []ServerOption{
	withGzip(),
	WithCodecs(
		encoding.GetCodec(encoding.CodecNameProto),
		encoding.GetCodec(encoding.CodecNameJSON),
		encoding.GetCodec(encoding.CodecNameJSONCharsetUTF8),
	),
}

func WithReadTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) { o.readTimeout = d }
}

func WithWriteTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) {
		o.writeTimeout = d
	}
}

func WithIdleTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) {
		o.idleTimeout = d
	}
}

func WithReadMaxBytes(n int) ServerOption {
	return func(o *serverOptions) {
		o.readMaxBytes = n
	}
}

func WithSendMaxBytes(n int) ServerOption {
	return func(o *serverOptions) {
		o.sendMaxBytes = n
	}
}

func WithBufferPool(pool mem.BufferPool) ServerOption {
	return func(o *serverOptions) {
		o.bufferPool = pool
	}
}
func WithStatsHandlers(handlers ...stats.EventHandler) ServerOption {
	return func(o *serverOptions) {
		o.statsHandlers = append(o.statsHandlers, handlers...)
	}
}

func WithUnknownHandler(h http.Handler) ServerOption {
	return func(o *serverOptions) {
		o.unknownHandler = h
	}
}

func WithCompression(name string, decompressor compress.Decompressor, compressor compress.Compressor) ServerOption {
	return func(o *serverOptions) {
		if o.compressionPools == nil {
			o.compressionPools = make(map[string]*compress.CompressionPool)
		}
		o.compressionNames = append(o.compressionNames, name)
		o.compressionPools[name] = compress.NewCompressionPool(compressor, decompressor, mem.DefaultBufferPool())
	}
}

func WithCompressionMinBytes(n int) ServerOption {
	return func(o *serverOptions) {
		o.compressMinBytes = n
	}
}

func WithCodecs(codecs ...encoding.Codec) ServerOption {
	return func(o *serverOptions) {
		if o.codecs == nil {
			o.codecs = make(map[string]encoding.Codec)
		}
		for _, codec := range codecs {
			if codec == nil {
				continue
			}
			name := codec.Name()
			if name == "" {
				continue
			}
			o.codecs[name] = codec
		}
	}
}

func withGzip() ServerOption {
	return WithCompression(
		compress.CompressionGzip,
		&gzip.Reader{},
		gzip.NewWriter(io.Discard),
	)
}

// WithInterceptors configures a client or handler's interceptor stack. Repeated
// WithInterceptors options are applied in order, so
//
//	WithInterceptors(A) + WithInterceptors(B, C) == WithInterceptors(A, B, C)
//
// Unary interceptors compose like an onion. The first interceptor provided is
// the outermost layer of the onion: it acts first on the context and request,
// and last on the response and error.
//
// Stream interceptors also behave like an onion: the first interceptor
// provided is the outermost wrapper for the [StreamingClientConn] or
// [StreamingHandlerConn]. It's the first to see sent messages and the last to
// see received messages.
//
// Applied to client and handler, WithInterceptors(A, B, ..., Y, Z) produces:
//
//	 client.Send()       client.Receive()
//	       |                   ^
//	       v                   |
//	    A ---                 --- A
//	    B ---                 --- B
//	    : ...                 ... :
//	    Y ---                 --- Y
//	    Z ---                 --- Z
//	       |                   ^
//	       v                   |
//	  = = = = = = = = = = = = = = = =
//	               network
//	  = = = = = = = = = = = = = = = =
//	       |                   ^
//	       v                   |
//	    A ---                 --- A
//	    B ---                 --- B
//	    : ...                 ... :
//	    Y ---                 --- Y
//	    Z ---                 --- Z
//	       |                   ^
//	       v                   |
//	handler.Receive()   handler.Send()
//	       |                   ^
//	       |                   |
//	       '-> handler logic >-'
//
// Note that in clients, Send handles the request message(s) and Receive
// handles the response message(s). For handlers, it's the reverse. Depending
// on your interceptor's logic, you may need to wrap one method in clients and
// the other in handlers.
func WithInterceptors(interceptors ...Interceptor) ServerOption {
	return func(o *serverOptions) {
		o.interceptor = Chain(append([]Interceptor{o.interceptor}, interceptors...)...)
	}
}
