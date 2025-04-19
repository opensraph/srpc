package protocol

import "net/url"

// Peer describes the other party to an RPC.
// When accessed client-side, Addr contains the host or host:port from the
// server's URL. When accessed server-side, Addr contains the client's address
// in IP:port format.
//
// On both the client and the server, Protocol is the RPC protocol in use.
// Currently, it's either [ProtocolConnect], [ProtocolGRPC], or
// [ProtocolGRPCWeb], but additional protocols may be added in the future.
//
// Query contains the query parameters for the request. For the server, this
// will reflect the actual query parameters sent. For the client, it is unset.
type Peer struct {
	// Addr contains the remote address:
	// - client-side: host or host:port from server URL
	// - server-side: client IP:port
	Addr string
	// Protocol indicates the RPC protocol in use (e.g., ProtocolConnect, ProtocolGRPC).
	Protocol string
	// Query contains request query parameters (server-side only).
	Query url.Values
}

func NewPeerFromURL(url *url.URL, protocol string) Peer {
	return Peer{
		Addr:     url.Host,
		Protocol: protocol,
	}
}
