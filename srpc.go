package srpc

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

type serviceDescriptor struct {
	ServiceName string
	Schema      protoreflect.ServiceDescriptor
	// Contains the implementation for the methods in this service.
	ServiceImpl any
	Metadata    any
	HandlerDesc map[string]*handlerDescriptor // method name => method desc
}

func newServiceDescriptor(sd *grpc.ServiceDesc, ss any) *serviceDescriptor {
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(sd.ServiceName))
	if err != nil {
		log.Fatalf("")
	}

	schema, ok := descriptor.(protoreflect.ServiceDescriptor)

	if !ok {
		log.Fatalf("")
	}

	serviceDesc := &serviceDescriptor{
		ServiceName: sd.ServiceName,
		Schema:      schema,
		ServiceImpl: ss,
		HandlerDesc: make(map[string]*handlerDescriptor),
		Metadata:    sd.Metadata,
	}
	for i := range sd.Methods {
		d := &sd.Methods[i]
		procedure := fmt.Sprintf("/%s/%s", sd.ServiceName, d.MethodName)
		hd := &handlerDescriptor{
			ServiceImpl:      ss,
			Procedure:        procedure,
			Schema:           schema.Methods().ByName(protoreflect.Name(d.MethodName)),
			StreamType:       protocol.StreamTypeUnary,
			IdempotencyLevel: protocol.IdempotencyNoSideEffects,
			Handler:          d.Handler,
		}
		serviceDesc.HandlerDesc[d.MethodName] = hd
	}
	for i := range sd.Streams {
		d := &sd.Streams[i]
		procedure := fmt.Sprintf("/%s/%s", sd.ServiceName, d.StreamName)
		hd := &handlerDescriptor{
			ServiceImpl:      ss,
			Procedure:        procedure,
			Schema:           schema.Methods().ByName(protoreflect.Name(d.StreamName)),
			StreamType:       streamTypeFromGRPCStreamDesc(d),
			IdempotencyLevel: protocol.IdempotencyUnknown,
			Handler:          d.Handler,
		}
		serviceDesc.HandlerDesc[d.StreamName] = hd
	}

	return serviceDesc
}

func (sd serviceDescriptor) NewHandler(opts serverOptions) http.Handler {
	if sd.HandlerDesc == nil {
		return http.NotFoundHandler()
	}

	handlers := make(map[string]http.Handler, len(sd.HandlerDesc))
	for _, hd := range sd.HandlerDesc {
		if procedure := hd.Procedure; procedure != "" {
			handlers[procedure] = hd.NewHandler(opts)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
}

type handlerDescriptor struct {
	// Contains the implementation for the methods in this service.
	ServiceImpl      any
	Procedure        string
	Schema           protoreflect.MethodDescriptor
	StreamType       protocol.StreamType
	IdempotencyLevel protocol.IdempotencyLevel
	Handler          any // the handler called for the method [grpc.StreamHandler] or [grpc.MethodHandler]
}

func (hd handlerDescriptor) NewHandler(srvOpts serverOptions) *Handler {
	handlers := hd.newProtocolHandlers(srvOpts)
	methodHandlers := mappedMethodHandlers(handlers)
	allowMethodValue := sortedAllowMethodValue(handlers)
	acceptPostValue := sortedAcceptPostValue(handlers)

	implementation := hd.newImplementation(srvOpts)

	return &Handler{
		ctx:              context.Background(),
		protocolHandlers: methodHandlers,
		allowMethod:      allowMethodValue,
		acceptPost:       acceptPostValue,
		streamType:       hd.StreamType,
		implementation:   implementation,
	}
}

type Implementation func(ctx context.Context, stream protocol.StreamingHandlerConn) error

func (hd *handlerDescriptor) newImplementation(srvOpts serverOptions) Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		switch hd.StreamType {
		case protocol.StreamTypeUnary:
			return hd.unaryImplementation(srvOpts)(ctx, conn)
		default:
			return hd.streamImplementation(srvOpts)(ctx, conn)
		}
	}
}

func (hd *handlerDescriptor) unaryImplementation(srvOpts serverOptions) Implementation {
	return func(ctx context.Context, coon protocol.StreamingHandlerConn) error {
		handler, ok := hd.Handler.(grpc.MethodHandler)
		if !ok {
			panic(errors.Newf("invalid unary handler type: %T", hd.Handler))
		}
		response, err := handler(hd.ServiceImpl, ctx, coon.Receive, srvOpts.interceptor.UnaryInterceptor())
		if err != nil {
			return err
		}
		return coon.Send(response)
	}
}

func (hd *handlerDescriptor) streamImplementation(srvOpts serverOptions) Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		handler, ok := hd.Handler.(grpc.StreamHandler)
		if !ok {
			return errors.Newf("invalid stream handler type: %T", hd.Handler)
		}

		streamInfo := &grpc.StreamServerInfo{
			FullMethod:     hd.Procedure,
			IsClientStream: hd.StreamType.IsClient(),
			IsServerStream: hd.StreamType.IsServer(),
		}

		// Apply stream interceptor if available
		if interceptor := srvOpts.interceptor.StreamInterceptor(); interceptor != nil {
			return interceptor(hd.ServiceImpl, newGRPCServerStreamBridge(ctx, conn), streamInfo, handler)
		}

		// Directly invoke the handler if no interceptor is present
		return handler(hd.ServiceImpl, newGRPCServerStreamBridge(ctx, conn))
	}
}

func (hd handlerDescriptor) newProtocolHandlers(srvOpts serverOptions) []protocol.ProtocolHandler {
	protocols := protocol.GetRegisteredProtocols()
	handlers := make([]protocol.ProtocolHandler, 0, len(protocols))
	compressors := compress.NewReadOnlyCompressionPools(
		srvOpts.compressionPools,
		srvOpts.compressionNames,
	)
	for _, p := range protocols {
		handlers = append(handlers, p.NewHandler(protocol.ProtocolHandlerParams{
			Spec:             hd.newSpec(),
			Codecs:           srvOpts.codecs,
			CompressionPools: compressors,
			CompressMinBytes: srvOpts.compressMinBytes,
			BufferPool:       srvOpts.bufferPool,
			ReadMaxBytes:     srvOpts.maxReceiveMessageSize,
			SendMaxBytes:     srvOpts.maxSendMessageSize,
			IdempotencyLevel: hd.IdempotencyLevel,
		}))
	}
	return handlers
}

func (hd handlerDescriptor) newSpec() protocol.Spec {
	return protocol.Spec{
		Procedure:        hd.Procedure,
		Schema:           hd.Schema,
		StreamType:       hd.StreamType,
		IdempotencyLevel: hd.IdempotencyLevel,
	}
}

func streamTypeFromGRPCStreamDesc(md *grpc.StreamDesc) protocol.StreamType {
	if md.ClientStreams && md.ServerStreams {
		return protocol.StreamTypeBidi
	} else if md.ClientStreams {
		return protocol.StreamTypeClient
	} else if md.ServerStreams {
		return protocol.StreamTypeServer
	}

	panic("grpc.StreamDesc ServerStreams and ClientStreams at least one must be true.")
}

func mappedMethodHandlers(handlers []protocol.ProtocolHandler) map[string][]protocol.ProtocolHandler {
	methodHandlers := make(map[string][]protocol.ProtocolHandler)
	for _, handler := range handlers {
		for method := range handler.Methods() {
			methodHandlers[method] = append(methodHandlers[method], handler)
		}
	}
	return methodHandlers
}

func sortedAllowMethodValue(handlers []protocol.ProtocolHandler) string {
	methods := make(map[string]struct{})
	for _, handler := range handlers {
		for method := range handler.Methods() {
			methods[method] = struct{}{}
		}
	}
	allow := make([]string, 0, len(methods))
	for ct := range methods {
		allow = append(allow, ct)
	}
	sort.Strings(allow)
	return strings.Join(allow, ", ")
}

func sortedAcceptPostValue(handlers []protocol.ProtocolHandler) string {
	contentTypes := make(map[string]struct{})
	for _, handler := range handlers {
		for contentType := range handler.ContentTypes() {
			contentTypes[contentType] = struct{}{}
		}
	}
	accept := make([]string, 0, len(contentTypes))
	for ct := range contentTypes {
		accept = append(accept, ct)
	}
	sort.Strings(accept)
	return strings.Join(accept, ", ")
}

var _ http.Handler = (*Handler)(nil)

type Handler struct {
	ctx              context.Context
	allowMethod      string                                // Allow header
	acceptPost       string                                // Accept-Post header
	protocolHandlers map[string][]protocol.ProtocolHandler // Method to protocol handlers

	streamType     protocol.StreamType // desc contains the stream description.
	implementation Implementation      // implementation is the function that implements the stream.
}

// ServeHTTP implements [http.Handler].
func (h *Handler) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	// We don't need to defer functions to close the request body or read to
	// EOF: the stream we construct later on already does that, and we only
	// return early when dealing with misbehaving clients. In those cases, it's
	// okay if we can't re-use the connection.
	isBidi := (h.streamType & protocol.StreamTypeBidi) == protocol.StreamTypeBidi
	if isBidi && request.ProtoMajor < 2 {
		// Clients coded to expect full-duplex connections may hang if they've
		// mistakenly negotiated HTTP/1.1. To unblock them, we must close the
		// underlying TCP connection.
		responseWriter.Header().Set("Connection", "close")
		responseWriter.WriteHeader(http.StatusHTTPVersionNotSupported)
		return
	}

	protocolHandlers, ok := h.protocolHandlers[request.Method]
	if !ok || len(protocolHandlers) == 0 {
		// No handlers for this method.
		responseWriter.Header().Set("Allow", h.allowMethod)
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	contentType := headers.CanonicalizeContentType(headers.GetHeaderCanonical(request.Header, headers.HeaderContentType))

	// Find our implementation of the RPC protocol in use.
	var protocolHandler protocol.ProtocolHandler
	for _, handler := range protocolHandlers {
		if handler.CanHandlePayload(request, contentType) {
			protocolHandler = handler
			break
		}
	}

	if protocolHandler == nil {
		responseWriter.Header().Set("Accept-Post", h.acceptPost)
		responseWriter.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}

	if request.Method == http.MethodGet {
		// A body must not be present.
		hasBody := request.ContentLength > 0
		if request.ContentLength < 0 {
			// No content-length header.
			// Test if body is empty by trying to read a single byte.
			var b [1]byte
			n, _ := request.Body.Read(b[:])
			hasBody = n > 0
		}
		if hasBody {
			responseWriter.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		_ = request.Body.Close()
	}

	// Establish a stream and serve the RPC.
	headers.SetHeaderCanonical(request.Header, headers.HeaderContentType, contentType)
	headers.SetHeaderCanonical(request.Header, headers.HeaderHost, request.Host)
	ctx, cancel, timeoutErr := protocolHandler.SetTimeout(request) //nolint: contextcheck
	if timeoutErr != nil {
		ctx = request.Context()
	}
	if cancel != nil {
		defer cancel()
	}
	connCloser, ok := protocolHandler.NewConn(
		responseWriter,
		request.WithContext(ctx),
	)
	if !ok {
		// Failed to create stream, usually because client used an unknown
		// compression algorithm. Nothing further to do.
		return
	}
	if timeoutErr != nil {
		_ = connCloser.Close(timeoutErr)
		return
	}

	ctx = metadata.NewIncomingContext(ctx, metadata.MD(request.Header))
	_ = connCloser.Close(h.implementation(ctx, connCloser))
}

var _ grpc.ServerStream = (*grpcServerStreamBridge)(nil)

type grpcServerStreamBridge struct {
	ctx    context.Context
	stream protocol.StreamingHandlerConn
}

func newGRPCServerStreamBridge(ctx context.Context, stream protocol.StreamingHandlerConn) grpc.ServerStream {
	return &grpcServerStreamBridge{ctx: ctx, stream: stream}
}

// Context implements grpc.ServerStream.
func (g *grpcServerStreamBridge) Context() context.Context {
	return g.ctx
}

// RecvMsg implements grpc.ServerStream.
func (g *grpcServerStreamBridge) RecvMsg(m any) error {
	err := g.stream.Receive(m)

	if err != nil && errors.Is(err, io.EOF) {
		return io.EOF
	}

	return err
}

// SendHeader implements grpc.ServerStream.
func (g *grpcServerStreamBridge) SendHeader(header metadata.MD) error {
	headers.MergeHeaders(g.stream.RequestHeader(), http.Header(header))
	return nil
}

// SendMsg implements grpc.ServerStream.
func (g *grpcServerStreamBridge) SendMsg(m any) error {
	return g.stream.Send(m)
}

// SetHeader implements grpc.ServerStream.
func (g *grpcServerStreamBridge) SetHeader(header metadata.MD) error {
	headers.MergeHeaders(g.stream.ResponseHeader(), http.Header(header))
	return nil
}

// SetTrailer implements grpc.ServerStream.
func (g *grpcServerStreamBridge) SetTrailer(trailer metadata.MD) {
	headers.MergeHeaders(g.stream.ResponseTrailer(), http.Header(trailer))
}
