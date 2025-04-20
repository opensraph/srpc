package srpc

import (
	"context"

	"github.com/opensraph/srpc/errors"
	"google.golang.org/grpc"
)

type Client interface {
	grpc.ClientConnInterface
}

var _ Client = (*client)(nil)

type client struct {
	conn *grpc.ClientConn
	opts clientOptions
}

func NewClient(target string, opt ...ClientOption) (*client, error) {
	opts := defaultClientOptions
	for _, o := range globalClientOptions {
		o(&opts)
	}
	for _, o := range opt {
		o(&opts)
	}

	if opts.interceptor.chainUnaryClientInts != nil && len(opts.interceptor.chainUnaryClientInts) > 0 {
		opts.grpcOpts = append(opts.grpcOpts, grpc.WithChainUnaryInterceptor(opts.interceptor.chainUnaryClientInts...))
	}
	if opts.interceptor.chainStreamClientInts != nil && len(opts.interceptor.chainStreamClientInts) > 0 {
		opts.grpcOpts = append(opts.grpcOpts, grpc.WithChainStreamInterceptor(opts.interceptor.chainStreamClientInts...))
	}

	conn, err := grpc.NewClient(target, opts.grpcOpts...)
	if err != nil {
		return nil, errors.Newf("failed to connect to %s: %v", target, err)
	}
	return &client{
		conn: conn,
	}, nil
}

// Invoke implements Client.
func (c *client) Invoke(ctx context.Context, method string, req, reply any, opts ...grpc.CallOption) error {
	return c.conn.Invoke(ctx, method, req, reply, opts...)
}

// NewStream implements Client.
func (c *client) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.conn.NewStream(ctx, desc, method, opts...)
}

func (c *client) Close() error {
	return c.conn.Close()
}
