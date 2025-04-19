package grpc

import (
	"fmt"
	"io"
	"net/http"
	"runtime"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
	"github.com/opensraph/srpc/version"
)

const (
	ProtocolGRPC    = protocol.ProtocolGRPC
	ProtocolGRPCWeb = protocol.ProtocolGRPCWeb
)

func init() {
	protocol.RegisterProtocol(&protocolGRPC{name: ProtocolGRPC, web: false})
	protocol.RegisterProtocol(&protocolGRPC{name: ProtocolGRPCWeb, web: true})
}

const (
	grpcHeaderCompression       = "Grpc-Encoding"
	grpcHeaderAcceptCompression = "Grpc-Accept-Encoding"
	grpcHeaderTimeout           = "Grpc-Timeout"
	grpcHeaderStatus            = "Grpc-Status"
	grpcHeaderMessage           = "Grpc-Message"
	grpcHeaderDetails           = "Grpc-Status-Details-Bin"

	grpcFlagEnvelopeTrailer = 0b10000000

	grpcContentTypeDefault    = "application/grpc"
	grpcWebContentTypeDefault = "application/grpc-web"
	grpcContentTypePrefix     = grpcContentTypeDefault + "+"
	grpcWebContentTypePrefix  = grpcWebContentTypeDefault + "+"

	headerXUserAgent = "X-User-Agent"

	upperHex = "0123456789ABCDEF"
)

var (
	errTrailersWithoutGRPCStatus = fmt.Errorf("protocol error: no %s trailer: %w", grpcHeaderStatus, io.ErrUnexpectedEOF)

	// defaultGrpcUserAgent follows
	// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#user-agents:
	//
	//	While the protocol does not require a user-agent to function it is recommended
	//	that clients provide a structured user-agent string that provides a basic
	//	description of the calling library, version & platform to facilitate issue diagnosis
	//	in heterogeneous environments. The following structure is recommended to library developers:
	//
	//	User-Agent → "grpc-" Language ?("-" Variant) "/" Version ?( " ("  *(AdditionalProperty ";") ")" )
	//
	//nolint:gochecknoglobals
	defaultGrpcUserAgent = fmt.Sprintf("grpc-go-connect/%s (%s)", version.Version, runtime.Version())
	//nolint:gochecknoglobals
	grpcAllowedMethods = map[string]struct{}{
		http.MethodPost: {},
	}
)

var _ protocol.Protocol = (*protocolGRPC)(nil)

type protocolGRPC struct {
	name string
	web  bool
}

// NewClient implements transport.Protocol.
func (g *protocolGRPC) NewClient(params protocol.ProtocolClientParams) (protocol.ProtocolClient, error) {
	peer := protocol.NewPeerFromURL(params.URL, ProtocolGRPC)
	if g.web {
		peer = protocol.NewPeerFromURL(params.URL, ProtocolGRPCWeb)
	}
	return &grpcClient{
		ProtocolClientParams: params,
		web:                  g.web,
		peer:                 peer,
	}, nil
}

// NewHandler implements transport.Protocol.
func (g *protocolGRPC) NewHandler(params protocol.ProtocolHandlerParams) protocol.ProtocolHandler {
	bare, prefix := grpcContentTypeDefault, grpcContentTypePrefix
	if g.web {
		bare, prefix = grpcWebContentTypeDefault, grpcWebContentTypePrefix
	}
	contentTypes := make(map[string]struct{})
	for _, name := range params.Codecs.Names() {
		contentTypes[headers.CanonicalizeContentType(prefix+name)] = struct{}{}
	}
	if params.Codecs.Get(encoding.CodecNameProto) != nil {
		contentTypes[bare] = struct{}{}
	}
	return &grpcHandler{
		ProtocolHandlerParams: params,
		web:                   g.web,
		accept:                contentTypes,
	}
}

// Name implements transport.Protocol.
func (g *protocolGRPC) Name() string {
	return g.name
}
