package srpc

import (
	"context"

	"google.golang.org/grpc"
)

// StreamingClientFunc is the generic signature of a streaming RPC from the client's
// perspective. Interceptors may wrap StreamingClientFuncs.
type StreamingClientFunc func(ctx context.Context, streamDesc StreamDesc) grpc.ClientStream

// An Interceptor adds logic to a generated handler or client, like the
// decorators or middleware you may have seen in other libraries. Interceptors
// may replace the context, mutate requests and responses, handle errors,
// retry, recover from panics, emit logs and metrics, or do nearly anything
// else.
//
// The returned functions must be safe to call concurrently.
type Interceptor interface {
	WrapUnary(next grpc.UnaryHandler) grpc.UnaryHandler
	WrapStreamingClient(next StreamingClientFunc) StreamingClientFunc
	WrapStreamingHandler(next grpc.StreamHandler) grpc.StreamHandler
}

func Chain(interceptors ...Interceptor) Interceptor {
	// We usually wrap in reverse order to have the first interceptor from
	// the slice act first. Rather than doing this dance repeatedly, reverse the
	// interceptor order now.
	var chain chain
	for i := len(interceptors) - 1; i >= 0; i-- {
		if interceptor := interceptors[i]; interceptor != nil {
			chain.interceptors = append(chain.interceptors, interceptor)
		}
	}
	return &chain
}

// A chain composes multiple interceptors into one.
type chain struct {
	interceptors []Interceptor
}

func (c *chain) WrapUnary(next grpc.UnaryHandler) grpc.UnaryHandler {
	for _, interceptor := range c.interceptors {
		next = interceptor.WrapUnary(next)
	}
	return next
}

func (c *chain) WrapStreamingClient(next StreamingClientFunc) StreamingClientFunc {
	for _, interceptor := range c.interceptors {
		next = interceptor.WrapStreamingClient(next)
	}
	return next
}

func (c *chain) WrapStreamingHandler(next grpc.StreamHandler) grpc.StreamHandler {
	for _, interceptor := range c.interceptors {
		next = interceptor.WrapStreamingHandler(next)
	}
	return next
}
