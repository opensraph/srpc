package srpc

import (
	"compress/gzip"
	"io"
	"math"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/internal/stats"
	"github.com/opensraph/srpc/mem"
	"google.golang.org/grpc/credentials"

	_ "github.com/opensraph/srpc/protocol/connect" // register connect protocol
	_ "github.com/opensraph/srpc/protocol/grpc"    // register grpc protocol

	_ "github.com/opensraph/srpc/encoding/protobinary" // register protobuf codec
	_ "github.com/opensraph/srpc/encoding/protojson"   // register json codec
)

const (
	defaultServerMaxConcurrentStreams  = math.MaxUint32
	defaultServerMaxReceiveMessageSize = 1024 * 1024 * 4
	defaultServerMaxSendMessageSize    = math.MaxInt32
)

type ServerOption func(o *serverOptions)

type serverOptions struct {
	sigs                  []os.Signal
	readTimeout           time.Duration
	writeTimeout          time.Duration
	idleTimeout           time.Duration
	maxConcurrentStreams  uint32
	maxReceiveMessageSize int
	maxSendMessageSize    int
	creds                 credentials.TransportCredentials

	numServerWorkers uint32

	eventHandlers []stats.EventHandler

	interceptor Interceptor

	unknownHandler http.Handler

	compressionNames []string
	compressionPools map[string]*compress.CompressionPool
	compressMinBytes int
	codecs           encoding.ReadOnlyCodecs

	bufferPool mem.BufferPool
}

var defaultServerOptions = serverOptions{
	sigs:                  []os.Signal{syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT},
	maxConcurrentStreams:  defaultServerMaxConcurrentStreams,
	maxReceiveMessageSize: defaultServerMaxReceiveMessageSize,
	maxSendMessageSize:    defaultServerMaxSendMessageSize,

	bufferPool: mem.DefaultBufferPool(),
	interceptor: Interceptor{
		chainUnaryServerInts:  make([]UnaryServerInterceptor, 0),
		chainStreamServerInts: make([]StreamServerInterceptor, 0),
	},
}

var globalServerOptions []ServerOption = []ServerOption{
	gzipCompression(),
	Codec(
		encoding.GetCodec(encoding.CodecNameProto),
		encoding.GetCodec(encoding.CodecNameJSON),
		encoding.GetCodec(encoding.CodecNameJSONCharsetUTF8),
	),
}

func ReadTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) { o.readTimeout = d }
}

func WriteTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) {
		o.writeTimeout = d
	}
}

func IdleTimeout(d time.Duration) ServerOption {
	return func(o *serverOptions) {
		o.idleTimeout = d
	}
}

// MaxConcurrentStreams returns a ServerOption that will apply a limit on the number
// of concurrent streams to each ServerTransport.
func MaxConcurrentStreams(n uint32) ServerOption {
	return func(o *serverOptions) {
		if n == 0 {
			n = defaultServerMaxConcurrentStreams
		}
		o.maxConcurrentStreams = n
	}
}

// MaxRecvMsgSize returns a ServerOption to set the max message size in bytes the server can receive.
// If this is not set, gRPC uses the default 4MB.
func MaxRecvMsgSize(n int) ServerOption {
	return func(o *serverOptions) {
		o.maxReceiveMessageSize = n
	}
}

// MaxSendMsgSize returns a ServerOption to set the max message size in bytes the server can send.
// If this is not set, gRPC uses the default `math.MaxInt32`.
func MaxSendMsgSize(n int) ServerOption {
	return func(o *serverOptions) {
		o.maxSendMessageSize = n
	}
}

// Creds returns a ServerOption that sets credentials for server connections.
func Creds(c credentials.TransportCredentials) ServerOption {
	return func(o *serverOptions) {
		o.creds = c
	}
}

// NumServerWorkers returns a ServerOption that sets the number of server workers.
func NumServerWorkers(n uint32) ServerOption {
	return func(o *serverOptions) {
		o.numServerWorkers = n
	}
}

// UnknownHandler returns a ServerOption that sets the handler for unknown
// requests. This is useful for handling requests that do not match any
// registered service or method. The handler should be a http.Handler that
// can handle the request and return a response. If not set, the server will
// return a 404 Not Found response for unknown requests.
func UnknownHandler(h http.Handler) ServerOption {
	return func(o *serverOptions) {
		o.unknownHandler = h
	}
}

func EventHandler(handlers ...stats.EventHandler) ServerOption {
	return func(o *serverOptions) {
		o.eventHandlers = append(o.eventHandlers, handlers...)
	}
}

func Compression(name string, decompressor compress.Decompressor, compressor compress.Compressor) ServerOption {
	return func(o *serverOptions) {
		if o.compressionPools == nil {
			o.compressionPools = make(map[string]*compress.CompressionPool)
		}
		o.compressionNames = append(o.compressionNames, name)
		o.compressionPools[name] = compress.NewCompressionPool(compressor, decompressor, mem.DefaultBufferPool())
	}
}

func gzipCompression() ServerOption {
	return Compression(compress.CompressionGzip, &gzip.Reader{}, gzip.NewWriter(io.Discard))
}

// CompressionMinBytes returns a ServerOption that sets the minimum number of bytes
// for compression to be applied. This is useful for tuning the performance of the
// server. If not set, the default value is 0, which means no minimum size.
// Compression will be applied to all messages.
func CompressionMinBytes(n int) ServerOption {
	return func(o *serverOptions) {
		o.compressMinBytes = n
	}
}

func Codec(codecs ...encoding.Codec) ServerOption {
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

// UnaryInterceptor returns a ServerOption that sets the UnaryServerInterceptor for the server.
func UnaryInterceptor(i UnaryServerInterceptor) ServerOption {
	return func(o *serverOptions) {
		o.interceptor.ChainUnaryInterceptor(i)
	}
}

// ChainUnaryInterceptor returns a ServerOption that specifies the chained interceptor
// for unary RPCs. The first interceptor will be the outer most,
// while the last interceptor will be the inner most wrapper around the real call.
// All unary interceptors added by this method will be chained.
func ChainUnaryInterceptor(ints ...UnaryServerInterceptor) ServerOption {
	return func(o *serverOptions) {
		o.interceptor.ChainUnaryInterceptor(ints...)
	}
}

// StreamInterceptor returns a ServerOption that sets the StreamServerInterceptor for the server.
func StreamInterceptor(i StreamServerInterceptor) ServerOption {
	return func(o *serverOptions) {
		o.interceptor.ChainStreamInterceptor(i)
	}
}

// ChainStreamInterceptor returns a ServerOption that specifies the chained interceptor
// for stream RPCs. The first interceptor will be the outer most,
// while the last interceptor will be the inner most wrapper around the real call.
// All stream interceptors added by this method will be chained.
func ChainStreamInterceptor(ints ...StreamServerInterceptor) ServerOption {
	return func(o *serverOptions) {
		o.interceptor.ChainStreamInterceptor(ints...)
	}
}
