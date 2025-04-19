package srpc

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	_ "github.com/opensraph/srpc/encoding/protobinary" // register protobuf codec
	_ "github.com/opensraph/srpc/encoding/protojson"   // register json codec
)

type ClientOption func(o *clientOptions)

type clientOptions struct {
	grpcOpts    []grpc.DialOption
	interceptor Interceptor
}

var defaultClientOptions = clientOptions{
	grpcOpts: make([]grpc.DialOption, 0),
}

var globalClientOptions []ClientOption = []ClientOption{
	WithGRPCOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
}

func WithGRPCOptions(opts ...grpc.DialOption) ClientOption {
	return func(o *clientOptions) {
		o.grpcOpts = append(o.grpcOpts, opts...)
	}
}

func WithClientInterceptors(interceptors ...Interceptor) ClientOption {
	return func(o *clientOptions) {
		o.interceptor = Chain(append([]Interceptor{o.interceptor}, interceptors...)...)
	}
}
