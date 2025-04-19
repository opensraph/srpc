package connect

import (
	"fmt"
	"net/http"
	"runtime"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
	"github.com/opensraph/srpc/version"

	_ "github.com/opensraph/srpc/encoding/protojson"
)

const (
	ProtocolConnect = protocol.ProtocolConnect

	connectUnaryHeaderCompression           = "Content-Encoding"
	connectUnaryHeaderAcceptCompression     = "Accept-Encoding"
	connectUnaryTrailerPrefix               = "Trailer-"
	connectStreamingHeaderCompression       = "Connect-Content-Encoding"
	connectStreamingHeaderAcceptCompression = "Connect-Accept-Encoding"
	connectHeaderTimeout                    = "Connect-Timeout-Ms"
	connectHeaderProtocolVersion            = "Connect-Protocol-Version"
	connectProtocolVersion                  = "1"
	headerVary                              = "Vary"

	connectFlagEnvelopeEndStream = 0b00000010

	connectUnaryContentTypePrefix     = "application/"
	connectUnaryContentTypeJSON       = connectUnaryContentTypePrefix + encoding.CodecNameJSON
	connectStreamingContentTypePrefix = "application/connect+"

	connectUnaryEncodingQueryParameter    = "encoding"
	connectUnaryMessageQueryParameter     = "message"
	connectUnaryBase64QueryParameter      = "base64"
	connectUnaryCompressionQueryParameter = "compression"
	connectUnaryConnectQueryParameter     = "connect"
	connectUnaryConnectQueryValue         = "v" + connectProtocolVersion
)

func init() {
	protocol.RegisterProtocol(&protocolConnect{})
}

// defaultConnectUserAgent returns a User-Agent string similar to those used in gRPC.
//
//nolint:gochecknoglobals
var defaultConnectUserAgent = fmt.Sprintf("connect-go/%s (%s)", version.Version, runtime.Version())

var _ protocol.Protocol = (*protocolConnect)(nil)

type protocolConnect struct{}

// NewHandler implements protocol, so it must return an interface.
func (*protocolConnect) NewHandler(params protocol.ProtocolHandlerParams) protocol.ProtocolHandler {
	methods := make(map[string]struct{})
	methods[http.MethodPost] = struct{}{}

	if params.Spec.StreamType == protocol.StreamTypeUnary && params.IdempotencyLevel == protocol.IdempotencyNoSideEffects {
		methods[http.MethodGet] = struct{}{}
	}

	contentTypes := make(map[string]struct{})
	for _, name := range params.Codecs.Names() {
		if params.Spec.StreamType == protocol.StreamTypeUnary {
			contentTypes[headers.CanonicalizeContentType(connectUnaryContentTypePrefix+name)] = struct{}{}
			continue
		}
		contentTypes[headers.CanonicalizeContentType(connectStreamingContentTypePrefix+name)] = struct{}{}
	}

	return &connectHandler{
		ProtocolHandlerParams: params,
		methods:               methods,
		accept:                contentTypes,
	}
}

// NewClient implements protocol, so it must return an interface.
func (*protocolConnect) NewClient(params protocol.ProtocolClientParams) (protocol.ProtocolClient, error) {
	return &connectClient{
		ProtocolClientParams: params,
		peer:                 protocol.NewPeerFromURL(params.URL, ProtocolConnect),
	}, nil
}

// Name implements transport.Protocol.
func (g *protocolConnect) Name() string {
	return ProtocolConnect
}
