package srpc

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
)

var _ http.Handler = (*Handler)(nil)

type Handler struct {
	ctx  context.Context
	opts *handlerOptions

	protocolHandlers map[string][]protocol.ProtocolHandler // Method to protocol handlers
	allowMethod      string                                // Allow header
	acceptPost       string                                // Accept-Post header
	desc             StreamDesc                            // desc contains the stream description.
	implementation   Implementation                        // implementation is the function that implements the stream.
}

// ServeHTTP implements [http.Handler].
func (h *Handler) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	// We don't need to defer functions to close the request body or read to
	// EOF: the stream we construct later on already does that, and we only
	// return early when dealing with misbehaving clients. In those cases, it's
	// okay if we can't re-use the connection.
	isBidi := (h.desc.StreamType & StreamTypeBidi) == StreamTypeBidi
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
	_ = connCloser.Close(h.implementation(ctx, connCloser))
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
