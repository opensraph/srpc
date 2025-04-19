package protocol

import (
	"context"
	"net/http"
	"net/url"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/mem"
)

// The names of the Connect, gRPC, and gRPC-Web protocols (as exposed by
// [Protocol]). Additional protocols may be added in the future.
const (
	ProtocolConnect = "connect"
	ProtocolGRPC    = "grpc"
	ProtocolGRPCWeb = "grpcweb"
)

var errNoTimeout = errors.New("no timeout")

// A Protocol defines the HTTP semantics to use when sending and receiving
// messages. It ties together codecs, compressors, and net/http to produce
// Senders and Receivers.
//
// For example, connect supports the gRPC protocol using this abstraction. Among
// many other things, the protocol implementation is responsible for
// translating timeouts from Go contexts to HTTP and vice versa. For gRPC, it
// converts timeouts to and from strings (for example, 10*time.Second <->
// "10S"), and puts those strings into the "Grpc-Timeout" HTTP header. Other
// protocols might encode durations differently, put them into a different HTTP
// header, or ignore them entirely.
//
// We don't have any short-term plans to export this interface; it's just here
// to separate the protocol-specific portions of connect from the
// protocol-agnostic plumbing.
type Protocol interface {
	NewHandler(ProtocolHandlerParams) ProtocolHandler
	NewClient(ProtocolClientParams) (ProtocolClient, error)
	Name() string
}

var registeredProtocols = make(map[string]Protocol)

func RegisterProtocol(p Protocol) {
	if p == nil {
		panic("cannot register a nil Protocol")
	}
	if p.Name() == "" {
		panic("cannot register Protocol with empty string result for Name()")
	}
	registeredProtocols[p.Name()] = p
}

func GetProtocol(name string) Protocol {
	return registeredProtocols[name]
}

func GetRegisteredProtocols() map[string]Protocol {
	return registeredProtocols
}

// HandlerParams are the arguments provided to a Protocol's NewHandler
// method, bundled into a struct to allow backward-compatible argument
// additions. Protocol implementations should take care to use the supplied
// Spec rather than constructing their own, since new fields may have been
// added.
type ProtocolHandlerParams struct {
	Spec             Spec
	Codecs           encoding.ReadOnlyCodecs
	CompressionPools compress.ReadOnlyCompressionPools
	CompressMinBytes int
	BufferPool       mem.BufferPool
	ReadMaxBytes     int
	SendMaxBytes     int
	IdempotencyLevel IdempotencyLevel
}

// ClientParams are the arguments provided to a Protocol's NewClient method,
// bundled into a struct to allow backward-compatible argument additions.
// Protocol implementations should take care to use the supplied Spec rather
// than constructing their own, since new fields may have been added.
type ProtocolClientParams struct {
	CompressionName  string
	CompressionPools compress.ReadOnlyCompressionPools
	Codec            encoding.Codec
	CompressMinBytes int
	HTTPClient       duplex.HTTPClient
	URL              *url.URL
	BufferPool       mem.BufferPool
	ReadMaxBytes     int
	SendMaxBytes     int
	EnableGet        bool
	GetURLMaxBytes   int
	GetUseFallback   bool
	// The gRPC family of protocols always needs access to a Protobuf codec to
	// marshal and unmarshal errors.
	Protobuf encoding.Codec
}

// Handler is the server side of a protocol. HTTP handlers typically support
// multiple protocols, codecs, and compressors.
type ProtocolHandler interface {
	// Methods is the set of HTTP methods the protocol can handle.
	Methods() map[string]struct{}

	// ContentTypes is the set of HTTP Content-Types that the protocol can
	// handle.
	ContentTypes() map[string]struct{}

	// SetTimeout runs before NewStream. Implementations may inspect the HTTP
	// request, parse any timeout set by the client, and return a modified
	// context and cancellation function.
	//
	// If the client didn't send a timeout, SetTimeout should return the
	// request's context, a nil cancellation function, and a nil error.
	SetTimeout(request *http.Request) (context.Context, context.CancelFunc, error)

	// CanHandlePayload returns true if the protocol can handle an HTTP request.
	// This is called after the request method is validated, so we only need to
	// be concerned with the content type/payload specifically.
	CanHandlePayload(request *http.Request, contentType string) bool

	// NewConn constructs a HandlerConn for the message exchange.
	NewConn(responseWriter http.ResponseWriter, request *http.Request) (HandlerConnCloser, bool)
}

// Client is the client side of a protocol. HTTP clients typically use a single
// protocol, codec, and compressor to send requests.
type ProtocolClient interface {
	// Peer describes the server for the RPC.
	Peer() Peer

	// WriteRequestHeader writes any protocol-specific request headers.
	WriteRequestHeader(streamType StreamType, header http.Header)

	// NewConn constructs a StreamingClientConn for the message exchange.
	//
	// Implementations should assume that the supplied HTTP headers have already
	// been populated by WriteRequestHeader. When constructing a stream for a
	// unary call, implementations may assume that the Sender's Send and Close
	// methods return before the Receiver's Receive or Close methods are called.
	NewConn(ctx context.Context, spec Spec, header http.Header) StreamingClientConn
}

// StreamingHandlerConn is the server's view of a bidirectional message
// exchange. Interceptors for streaming RPCs may wrap StreamingHandlerConns.
//
// Like the standard library's [http.ResponseWriter], StreamingHandlerConns write
// response headers to the network with the first call to Send. Any subsequent
// mutations are effectively no-ops. Handlers may mutate response trailers at
// any time before returning. When the client has finished sending data,
// Receive returns an error wrapping [io.EOF]. Handlers should check for this
// using the standard library's [errors.Is].
//
// Headers and trailers beginning with "Connect-" and "Grpc-" are reserved for
// use by the gRPC and Connect protocols: applications may read them but
// shouldn't write them.
//
// StreamingHandlerConn implementations provided by this module guarantee that
// all returned errors can be cast to [*Error] using the standard library's
// [errors.As].
//
// StreamingHandlerConn implementations do not need to be safe for concurrent use.
type StreamingHandlerConn interface {
	Spec() Spec
	Peer() Peer

	Receive(msg any) error
	RequestHeader() http.Header

	Send(msg any) error
	ResponseHeader() http.Header
	ResponseTrailer() http.Header
}

// HandlerConnCloser extends StreamingHandlerConn with a method for handlers to
// terminate the message exchange (and optionally send an error to the client).
type HandlerConnCloser interface {
	StreamingHandlerConn

	Close(err error) error
}

// StreamingClientConn is the client's view of a bidirectional message exchange.
// Interceptors for streaming RPCs may wrap StreamingClientConns.
//
// StreamingClientConns write request headers to the network with the first
// call to Send. Any subsequent mutations are effectively no-ops. When the
// server is done sending data, the StreamingClientConn's Receive method
// returns an error wrapping [io.EOF]. Clients should check for this using the
// standard library's [errors.Is]. If the server encounters an error during
// processing, subsequent calls to the StreamingClientConn's Send method will
// return an error wrapping [io.EOF]; clients may then call Receive to unmarshal
// the error.
//
// Headers and trailers beginning with "Connect-" and "Grpc-" are reserved for
// use by the gRPC and Connect protocols: applications may read them but
// shouldn't write them.
//
// StreamingClientConn implementations provided by this module guarantee that
// all returned errors can be cast to [*Error] using the standard library's
// [errors.As].
//
// In order to support bidirectional streaming RPCs, all StreamingClientConn
// implementations must support limited concurrent use. See the comments on
// each group of methods for details.
type StreamingClientConn interface {
	// Spec and Peer must be safe to call concurrently with all other methods.
	Spec() Spec
	Peer() Peer

	// Send, RequestHeader, and CloseRequest may race with each other, but must
	// be safe to call concurrently with all other methods.
	Send(msg any) error
	RequestHeader() http.Header
	CloseRequest() error

	// Receive, ResponseHeader, ResponseTrailer, and CloseResponse may race with
	// each other, but must be safe to call concurrently with all other methods.
	Receive(msg any) error
	ResponseHeader() http.Header
	ResponseTrailer() http.Header
	CloseResponse() error

	OnRequestSend(fn func(*http.Request))
}
