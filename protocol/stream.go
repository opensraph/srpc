package protocol

import "fmt"

/* Streaming Mode
 ---------------  [Unary Streaming]  ---------------
 --------------- (Req) returns (Res) ---------------
 client.Send(req)   === req ==>   server.Recv(req)
 client.Recv(res)   <== res ===   server.Send(res)


 ------------------- [Client Streaming] -------------------
 --------------- (stream Req) returns (Res) ---------------
 client.Send(req)         === req ==>       server.Recv(req)
							  ...
 client.Send(req)         === req ==>       server.Recv(req)

 client.CloseSend()       === EOF ==>       server.Recv(EOF)
 client.Recv(res)         <== res ===       server.SendAndClose(res)
 ** OR
 client.CloseAndRecv(res) === EOF ==>       server.Recv(EOF)
						  <== res ===       server.SendAndClose(res)


 ------------------- [Server Streaming] -------------------
 ---------- (Request) returns (stream Response) ----------
 client.Send(req)   === req ==>   server.Recv(req)
 client.CloseSend() === EOF ==>   server.Recv(EOF)
 client.Recv(res)   <== res ===   server.Send(req)
						...
 client.Recv(res)   <== res ===   server.Send(req)
 client.Recv(EOF)   <== EOF ===   server handler return


 ----------- [Bidirectional Streaming] -----------
 --- (stream Request) returns (stream Response) ---
 * goroutine 1 *
 client.Send(req)   === req ==>   server.Recv(req)
						...
 client.Send(req)   === req ==>   server.Recv(req)
 client.CloseSend() === EOF ==>   server.Recv(EOF)

 * goroutine 2 *
 client.Recv(res)   <== res ===   server.Send(req)
						...
 client.Recv(res)   <== res ===   server.Send(req)
 client.Recv(EOF)   <== EOF ===   server handler return
*/

// StreamType describes whether the client, server, neither, or both is
// streaming.
type StreamType uint8

const (
	// StreamTypeUnary indicates a non-streaming RPC.
	StreamTypeUnary StreamType = 0b00
	// StreamTypeClient indicates client-side streaming.
	StreamTypeClient StreamType = 0b01
	// StreamTypeServer indicates server-side streaming.
	StreamTypeServer StreamType = 0b10
	// StreamTypeBidi indicates bidirectional streaming.
	StreamTypeBidi = StreamTypeClient | StreamTypeServer
)

func (s StreamType) String() string {
	switch s {
	case StreamTypeUnary:
		return "unary"
	case StreamTypeClient:
		return "client"
	case StreamTypeServer:
		return "server"
	case StreamTypeBidi:
		return "bidi"
	}
	return fmt.Sprintf("stream_%d", s)
}
