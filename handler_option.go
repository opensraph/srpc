package srpc

import (
	"strings"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/protocol"
)

// HandlerOption is a function that configures the handler options.
type HandlerOption func(o *handlerOptions)

type handlerOptions struct {
	procedure        string
	schema           any
	streamType       StreamType
	idempotencyLevel IdempotencyLevel
	srvOpts          serverOptions
}

func newHandlerOption(desc StreamDesc, srvOpts serverOptions, options []HandlerOption) *handlerOptions {
	protoPath := extractProtoPath(desc.Procedure)
	o := handlerOptions{
		procedure:  protoPath,
		streamType: desc.StreamType,
	}
	for _, opt := range options {
		opt(&o)
	}
	return &o
}

func (c *handlerOptions) newProtocolHandlers() []protocol.ProtocolHandler {
	protocols := protocol.GetRegisteredProtocols()
	handlers := make([]protocol.ProtocolHandler, 0, len(protocols))
	compressors := compress.NewReadOnlyCompressionPools(
		c.srvOpts.compressionPools,
		c.srvOpts.compressionNames,
	)
	for _, p := range protocols {
		handlers = append(handlers, p.NewHandler(protocol.ProtocolHandlerParams{
			Spec:             c.newSpec(),
			Codecs:           c.srvOpts.codecs,
			CompressionPools: compressors,
			CompressMinBytes: c.srvOpts.compressMinBytes,
			BufferPool:       c.srvOpts.bufferPool,
			ReadMaxBytes:     c.srvOpts.readMaxBytes,
			SendMaxBytes:     c.srvOpts.sendMaxBytes,
			IdempotencyLevel: protocol.IdempotencyLevel(c.idempotencyLevel),
		}))
	}
	return handlers
}

func (c *handlerOptions) newSpec() protocol.Spec {
	return protocol.Spec{
		Procedure:        c.procedure,
		Schema:           c.schema,
		StreamType:       protocol.StreamType(c.streamType),
		IdempotencyLevel: protocol.IdempotencyLevel(c.idempotencyLevel),
	}
}

// extractProtoPath returns the trailing portion of the URL's path,
// corresponding to the Protobuf package, service, and method. It always starts
// with a slash. Within connect, we use this as (1) Spec.Procedure and (2) the
// path when mounting handlers on muxes.
func extractProtoPath(path string) string {
	segments := strings.Split(path, "/")
	var pkg, method string
	if len(segments) > 0 {
		pkg = segments[0]
	}
	if len(segments) > 1 {
		pkg = segments[len(segments)-2]
		method = segments[len(segments)-1]
	}
	if pkg == "" {
		return "/"
	}
	if method == "" {
		return "/" + pkg
	}
	return "/" + pkg + "/" + method
}
