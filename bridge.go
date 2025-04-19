package srpc

import (
	"context"
	"net/http"

	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var _ grpc.ServerStream = (*grpcServerStreamBridge)(nil)

type grpcServerStreamBridge struct {
	ctx    context.Context
	stream protocol.StreamingHandlerConn
}

// Context implements grpc.ServerStream.
func (g *grpcServerStreamBridge) Context() context.Context {
	return g.ctx
}

// RecvMsg implements grpc.ServerStream.
func (g *grpcServerStreamBridge) RecvMsg(m any) error {
	return g.stream.Receive(m)
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

func newGRPCServerStreamBridge(ctx context.Context, stream protocol.StreamingHandlerConn) grpc.ServerStream {
	return &grpcServerStreamBridge{ctx: ctx, stream: stream}
}
