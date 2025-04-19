package srpc

import (
	"context"
	"fmt"

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

func NewClient(target string, opt ...ClientOption) *client {
	opts := defaultClientOptions
	for _, o := range globalClientOptions {
		o(&opts)
	}
	for _, o := range opt {
		o(&opts)
	}
	conn, err := grpc.NewClient(target, opts.grpcOpts...)
	if err != nil {
		panic(errors.Newf("failed to connect to %s: %v", target, err))
	}
	return &client{
		conn: conn,
	}
}

// Invoke implements Client.
func (c *client) Invoke(ctx context.Context, method string, req, reply any, opts ...grpc.CallOption) error {
	next := c.conn.Invoke
	if interceptor := c.opts.interceptor; interceptor != nil {
		handler := func(ctx context.Context, req any) (any, error) {
			err := next(ctx, method, req, reply, opts...)
			return reply, err
		}

		wrappedHandler := interceptor.WrapUnary(handler)
		_, err := wrappedHandler(ctx, req)
		return err
	}

	return next(ctx, method, req, reply, opts...)
}

// NewStream implements Client.
func (c *client) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if interceptor := c.opts.interceptor; interceptor != nil {
		var clientStream grpc.ClientStream
		var err error
		handler := StreamingClientFunc(func(ctx context.Context, streamDesc StreamDesc) grpc.ClientStream {
			clientStream, err = c.conn.NewStream(ctx, desc, method, opts...)
			return clientStream
		})

		wrappedHandler := interceptor.WrapStreamingClient(handler)

		// 调用包装后的处理函数
		return wrappedHandler(ctx, StreamDesc{
			StreamType: parseGrpcStreamType(desc),
			Procedure:  fmt.Sprintf("%s/%s", desc.StreamName, method),
			Handler:    desc.Handler,
			IsClient:   true,
		}), err
	}

	// 没有拦截器时，直接调用原始方法
	return c.conn.NewStream(ctx, desc, method, opts...)
}
