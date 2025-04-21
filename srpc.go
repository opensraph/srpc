package srpc

import (
	"context"
	"fmt"
	"io"
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

// serviceDescriptor represents a gRPC service with its schema and implementation details.
// It maps method names to their handler descriptors.
type serviceDescriptor struct {
	serviceName string                         // The fully qualified name of the service
	schema      protoreflect.ServiceDescriptor // Protobuf service descriptor containing the service schema
	serviceImpl any                            // The service implementation object
	metadata    any                            // Additional metadata associated with the service
	handlers    map[string]http.Handler        // Map of procedure names to HTTP handlers
	handlerDesc map[string]*handlerDescriptor  // Method name to method descriptor mapping

	opts serverOptions // Server options for configuring the service
}

// newServiceDescriptor creates a new serviceDescriptor from a gRPC ServiceDesc and implementation.
// It builds handler descriptors for all methods and streams defined in the service.
func newServiceDescriptor(sd *grpc.ServiceDesc, ss any, opts serverOptions) (*serviceDescriptor, error) {
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(sd.ServiceName))
	if err != nil {
		return nil, errors.Newf("Failed to find service descriptor: %v", err)
	}

	schema, ok := descriptor.(protoreflect.ServiceDescriptor)

	if !ok {
		return nil, errors.Newf("Descriptor is not a service descriptor: %T", descriptor)
	}

	serviceDesc := &serviceDescriptor{
		serviceName: sd.ServiceName,
		schema:      schema,
		serviceImpl: ss,
		metadata:    sd.Metadata,
		handlerDesc: make(map[string]*handlerDescriptor),
		handlers:    make(map[string]http.Handler),
	}

	// Register all unary methods
	for i := range sd.Methods {
		d := &sd.Methods[i]
		procedure := fmt.Sprintf("/%s/%s", sd.ServiceName, d.MethodName)
		hd := &handlerDescriptor{
			serviceImpl:      ss,
			procedure:        procedure,
			schema:           schema.Methods().ByName(protoreflect.Name(d.MethodName)),
			streamType:       protocol.StreamTypeUnary,
			idempotencyLevel: protocol.IdempotencyNoSideEffects,
			handler:          d.Handler,
			opts:             opts,
		}
		serviceDesc.handlerDesc[d.MethodName] = hd
		serviceDesc.handlers[procedure] = hd.NewHandler()
	}

	// Register all streaming methods
	for i := range sd.Streams {
		d := &sd.Streams[i]
		procedure := fmt.Sprintf("/%s/%s", sd.ServiceName, d.StreamName)
		hd := &handlerDescriptor{
			serviceImpl:      ss,
			procedure:        procedure,
			schema:           schema.Methods().ByName(protoreflect.Name(d.StreamName)),
			streamType:       convertToStreamType(d),
			idempotencyLevel: protocol.IdempotencyUnknown,
			handler:          d.Handler,
			opts:             opts,
		}
		serviceDesc.handlerDesc[d.StreamName] = hd
		serviceDesc.handlers[procedure] = hd.NewHandler()
	}

	return serviceDesc, nil
}

// NewHandler creates an HTTP handler that routes requests to the appropriate method handler
// based on the URL path matching the procedure name.
func (sd serviceDescriptor) NewHandler() http.Handler {
	if sd.handlers == nil || len(sd.handlers) == 0 {
		return sd.opts.unknownHandler
	}
	// Return a handler function that routes to the appropriate method handler
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := sd.handlers[r.URL.Path]; ok {
			h.ServeHTTP(w, r)
		} else {
			sd.opts.unknownHandler.ServeHTTP(w, r)
		}
	})
}

// handlerDescriptor contains all information needed to handle a specific RPC method.
// It includes method metadata and the implementation handler.
type handlerDescriptor struct {
	// Contains the implementation for the methods in this service.
	serviceImpl      any                           // The service implementation object
	procedure        string                        // The full procedure path (e.g., "/service/method")
	schema           protoreflect.MethodDescriptor // Method descriptor from protobuf schema
	streamType       protocol.StreamType           // Type of streaming (unary, client, server, or bidi)
	idempotencyLevel protocol.IdempotencyLevel     // Idempotency level of the method
	handler          any                           // The handler function (either grpc.StreamHandler or grpc.MethodHandler)

	opts serverOptions // Server options for configuring the service
}

// NewHandler creates a new HTTP handler for this specific RPC method.
// It configures protocol handlers, allowed methods, and the implementation function.
func (hd handlerDescriptor) NewHandler() *Handler {
	// Create protocol handlers and derive metadata from them
	handlers := hd.newProtocolHandlers()
	methodHandlers := mappedMethodHandlers(handlers)
	allowMethodValue := sortedAllowMethodValue(handlers)
	acceptPostValue := sortedAcceptPostValue(handlers)

	// Get the implementation function that will process the RPC
	implementation := hd.newImplementation()

	return &Handler{
		ctx:              context.Background(),
		protocolHandlers: methodHandlers,
		allowMethod:      allowMethodValue,
		acceptPost:       acceptPostValue,
		streamType:       hd.streamType,
		implementation:   implementation,
	}
}

// Implementation is a function type that handles an RPC call with the given context and stream.
type Implementation func(ctx context.Context, stream protocol.StreamingHandlerConn) error

// newImplementation returns an implementation function for handling the RPC.
// It dispatches to the appropriate implementation based on stream type.
func (hd *handlerDescriptor) newImplementation() Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		switch hd.streamType {
		case protocol.StreamTypeUnary:
			return hd.unaryImplementation()(ctx, conn)
		default:
			return hd.streamImplementation()(ctx, conn)
		}
	}
}

// unaryImplementation returns an implementation function for handling unary RPCs.
// It unpacks the request, calls the handler, and sends back the response.
func (hd *handlerDescriptor) unaryImplementation() Implementation {
	handler, ok := hd.handler.(grpc.MethodHandler)
	if !ok {
		panic(errors.Newf("invalid unary handler type: %T", hd.handler))
	}
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		// Call the handler with interceptor (if available)
		response, err := handler(hd.serviceImpl, ctx, conn.Receive, hd.opts.interceptor.UnaryInterceptor())
		if err != nil {
			return err
		}

		// Send the response back to the client
		return conn.Send(response)
	}
}

func (hd *handlerDescriptor) streamImplementation() Implementation {
	handler, ok := hd.handler.(grpc.StreamHandler)
	if !ok {
		panic(errors.Newf("invalid stream handler type: %T", hd.handler))
	}
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {

		streamInfo := &grpc.StreamServerInfo{
			FullMethod:     hd.procedure,
			IsClientStream: hd.streamType.IsClient(),
			IsServerStream: hd.streamType.IsServer(),
		}

		// Apply stream interceptor if available
		if interceptor := hd.opts.interceptor.StreamInterceptor(); interceptor != nil {
			return interceptor(hd.serviceImpl, newGRPCServerStreamBridge(ctx, conn), streamInfo, handler)
		}

		// Directly invoke the handler if no interceptor is present
		return handler(hd.serviceImpl, newGRPCServerStreamBridge(ctx, conn))
	}
}

func (hd handlerDescriptor) newProtocolHandlers() []protocol.ProtocolHandler {
	protocols := protocol.GetRegisteredProtocols()
	handlers := make([]protocol.ProtocolHandler, 0, len(protocols))
	compressors := compress.NewReadOnlyCompressionPools(
		hd.opts.compressionPools,
		hd.opts.compressionNames,
	)
	for _, p := range protocols {
		handlers = append(handlers, p.NewHandler(protocol.ProtocolHandlerParams{
			Spec:             hd.newSpec(),
			Codecs:           hd.opts.codecs,
			CompressionPools: compressors,
			CompressMinBytes: hd.opts.compressMinBytes,
			BufferPool:       hd.opts.bufferPool,
			ReadMaxBytes:     hd.opts.maxReceiveMessageSize,
			SendMaxBytes:     hd.opts.maxSendMessageSize,
			IdempotencyLevel: hd.idempotencyLevel,
		}))
	}
	return handlers
}

func (hd handlerDescriptor) newSpec() protocol.Spec {
	return protocol.Spec{
		Procedure:        hd.procedure,
		Schema:           hd.schema,
		StreamType:       hd.streamType,
		IdempotencyLevel: hd.idempotencyLevel,
	}
}

// convertToStreamType converts a gRPC StreamDesc to the appropriate protocol.StreamType.
// It determines the stream type based on whether client streaming and/or server streaming is enabled.
func convertToStreamType(md *grpc.StreamDesc) protocol.StreamType {
	if md == nil {
		panic("convertToStreamType: md cannot be nil")
	}
	switch {
	case md.ClientStreams && md.ServerStreams:
		return protocol.StreamTypeBidi
	case md.ClientStreams:
		return protocol.StreamTypeClient
	case md.ServerStreams:
		return protocol.StreamTypeServer
	default:
		panic("convertToStreamType: invalid gRPC.StreamDesc, at least one of ServerStreams or ClientStreams must be true")
	}
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

// Handler is an HTTP handler that serves RPC requests.
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

// grpcServerStreamBridge bridges gRPC server streams to the internal streaming handler connection.
type grpcServerStreamBridge struct {
	ctx    context.Context
	stream protocol.StreamingHandlerConn
}

// newGRPCServerStreamBridge creates a new grpcServerStreamBridge.
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
