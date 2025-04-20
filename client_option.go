package srpc

import (
	_ "github.com/opensraph/srpc/encoding/protobinary" // register protobuf codec
	_ "github.com/opensraph/srpc/encoding/protojson"   // register json codec
	"google.golang.org/grpc"
)

type ClientOption func(o *clientOptions)

type DialOption = grpc.DialOption

type clientOptions struct {
	grpcOpts    []DialOption
	interceptor Interceptor
}

var defaultClientOptions = clientOptions{
	grpcOpts: make([]DialOption, 0),
	interceptor: Interceptor{
		chainUnaryClientInts:  make([]UnaryClientInterceptor, 0),
		chainStreamClientInts: make([]StreamClientInterceptor, 0),
	},
}

var globalClientOptions []ClientOption = []ClientOption{}

func WithGRPCOptions(opts ...DialOption) ClientOption {
	return func(o *clientOptions) {
		o.grpcOpts = append(o.grpcOpts, opts...)
	}
}

func WithUnaryInterceptor(interceptor UnaryClientInterceptor) ClientOption {
	return func(o *clientOptions) {
		o.interceptor.chainUnaryClientInts = append(o.interceptor.chainUnaryClientInts, interceptor)
	}
}

func WithChainUnaryInterceptor(Interceptors ...UnaryClientInterceptor) ClientOption {
	return func(o *clientOptions) {
		o.interceptor.chainUnaryClientInts = append(o.interceptor.chainUnaryClientInts, Interceptors...)
	}
}

func WithStreamInterceptor(interceptor StreamClientInterceptor) ClientOption {
	return func(o *clientOptions) {
		o.interceptor.chainStreamClientInts = append(o.interceptor.chainStreamClientInts, interceptor)
	}
}

func WithChainStreamInterceptor(Interceptors ...StreamClientInterceptor) ClientOption {
	return func(o *clientOptions) {
		o.interceptor.chainStreamClientInts = append(o.interceptor.chainStreamClientInts, Interceptors...)
	}
}
