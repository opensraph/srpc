package srpc

import (
	"context"

	"google.golang.org/grpc"
)

// Interceptor manages the RPC interceptor chains for both server and client-side
// interceptors, supporting both unary and streaming modes. It provides chainable
// methods to add multiple interceptors that will be executed in sequence.
//
// Interceptors follow an "onion model" execution pattern:
// - For servers: The first added interceptor processes the request first and the response last
// - For clients: The first added interceptor processes outgoing messages first and incoming messages last
//
// Request flow visualization:
//
//	 client.Send()       client.Receive()
//	       |                   ^
//	       v                   |
//	    A ---                 --- A
//	    B ---                 --- B
//	    : ...                 ... :
//	    Y ---                 --- Y
//	    Z ---                 --- Z
//	       |                   ^
//	       v                   |
//	  = = = = = = = = = = = = = = = =
//	               network
//	  = = = = = = = = = = = = = = = =
//	       |                   ^
//	       v                   |
//	    A ---                 --- A
//	    B ---                 --- B
//	    : ...                 ... :
//	    Y ---                 --- Y
//	    Z ---                 --- Z
//	       |                   ^
//	       v                   |
//	handler.Receive()   handler.Send()
//	       |                   ^
//	       |                   |
//	       '-> handler logic >-'
//
// Note: In clients, Send handles request messages and Receive handles response messages.
// For handlers, it's the reverse. Depending on your interceptor's logic, you may need
// to wrap different methods in clients versus servers.
//
// Interceptors are commonly used for implementing cross-cutting concerns like logging,
// authentication, error handling, metrics collection, and tracing.
type Interceptor struct {
	chainUnaryServerInts  []UnaryServerInterceptor
	chainStreamServerInts []StreamServerInterceptor
	chainUnaryClientInts  []UnaryClientInterceptor
	chainStreamClientInts []StreamClientInterceptor
}

type UnaryHandler = grpc.UnaryHandler
type UnaryServerInfo = grpc.UnaryServerInfo
type UnaryServerInterceptor = grpc.UnaryServerInterceptor

// ChainUnaryInterceptor adds unary server interceptors to the chain.
// See [grpc.ChainUnaryInterceptor] for more details.
func (i *Interceptor) ChainUnaryInterceptor(ints ...UnaryServerInterceptor) *Interceptor {
	i.chainUnaryServerInts = append(i.chainUnaryServerInts, ints...)
	return i
}

func (i *Interceptor) UnaryInterceptor() UnaryServerInterceptor {
	interceptors := i.chainUnaryServerInts
	var chainedInt UnaryServerInterceptor
	if len(interceptors) == 0 {
		chainedInt = nil
	} else if len(interceptors) == 1 {
		chainedInt = interceptors[0]
	} else {
		chainedInt = chainUnaryInterceptors(interceptors)
	}
	return chainedInt
}

func chainUnaryInterceptors(interceptors []UnaryServerInterceptor) UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *UnaryServerInfo, handler UnaryHandler) (any, error) {
		return interceptors[0](ctx, req, info, getChainUnaryHandler(interceptors, 0, info, handler))
	}
}

func getChainUnaryHandler(interceptors []UnaryServerInterceptor, curr int, info *UnaryServerInfo, finalHandler UnaryHandler) UnaryHandler {
	if curr == len(interceptors)-1 {
		return finalHandler
	}
	return func(ctx context.Context, req any) (any, error) {
		return interceptors[curr+1](ctx, req, info, getChainUnaryHandler(interceptors, curr+1, info, finalHandler))
	}
}

type ServerStream = grpc.ServerStream
type StreamHandler = grpc.StreamHandler
type StreamServerInfo = grpc.StreamServerInfo
type StreamServerInterceptor = grpc.StreamServerInterceptor

// ChainStreamInterceptor adds stream server interceptors to the chain.
// See [grpc.ChainStreamInterceptor] for more details.
func (i *Interceptor) ChainStreamInterceptor(ints ...StreamServerInterceptor) *Interceptor {
	i.chainStreamServerInts = append(i.chainStreamServerInts, ints...)
	return i
}

func (i *Interceptor) StreamInterceptor() StreamServerInterceptor {
	interceptors := i.chainStreamServerInts
	var chainedInt StreamServerInterceptor
	if len(interceptors) == 0 {
		chainedInt = nil
	} else if len(interceptors) == 1 {
		chainedInt = interceptors[0]
	} else {
		chainedInt = chainStreamInterceptors(interceptors)
	}
	return chainedInt
}

func chainStreamInterceptors(interceptors []StreamServerInterceptor) StreamServerInterceptor {
	return func(srv any, ss ServerStream, info *StreamServerInfo, handler StreamHandler) error {
		return interceptors[0](srv, ss, info, getChainStreamHandler(interceptors, 0, info, handler))
	}
}

func getChainStreamHandler(interceptors []StreamServerInterceptor, curr int, info *StreamServerInfo, finalHandler StreamHandler) StreamHandler {
	if curr == len(interceptors)-1 {
		return finalHandler
	}
	return func(srv any, stream ServerStream) error {
		return interceptors[curr+1](srv, stream, info, getChainStreamHandler(interceptors, curr+1, info, finalHandler))
	}
}

type UnaryClientInterceptor = grpc.UnaryClientInterceptor
type UnaryInvoker = grpc.UnaryInvoker
type CallOption = grpc.CallOption
type ClientConn = grpc.ClientConn

// WithChainUnaryInterceptor adds unary client interceptors to the chain.
// See [WithChainUnaryInterceptor] for more details.
func (i *Interceptor) WithChainUnaryInterceptor(ints ...UnaryClientInterceptor) *Interceptor {
	i.chainUnaryClientInts = append(i.chainUnaryClientInts, ints...)
	return i
}

func (i *Interceptor) UnaryClientInterceptor() UnaryClientInterceptor {
	interceptors := i.chainUnaryClientInts
	var chainedInt UnaryClientInterceptor
	if len(interceptors) == 0 {
		chainedInt = nil
	} else if len(interceptors) == 1 {
		chainedInt = interceptors[0]
	} else {
		chainedInt = func(ctx context.Context, method string, req, reply any, cc *ClientConn, invoker UnaryInvoker, opts ...CallOption) error {
			return interceptors[0](ctx, method, req, reply, cc, getChainUnaryInvoker(interceptors, 0, invoker), opts...)
		}
	}
	return chainedInt
}

func getChainUnaryInvoker(interceptors []UnaryClientInterceptor, curr int, finalInvoker UnaryInvoker) UnaryInvoker {
	if curr == len(interceptors)-1 {
		return finalInvoker
	}
	return func(ctx context.Context, method string, req, reply any, cc *ClientConn, opts ...CallOption) error {
		return interceptors[curr+1](ctx, method, req, reply, cc, getChainUnaryInvoker(interceptors, curr+1, finalInvoker), opts...)
	}
}

type StreamClientInterceptor = grpc.StreamClientInterceptor
type ClientStream = grpc.ClientStream
type Streamer = grpc.Streamer
type GRPCStreamDesc = grpc.StreamDesc

// WithChainStreamInterceptor adds stream client interceptors to the chain.
// See [grpc.WithChainStreamInterceptor] for more details.
func (i *Interceptor) WithChainStreamInterceptor(ints ...StreamClientInterceptor) *Interceptor {
	i.chainStreamClientInts = append(i.chainStreamClientInts, ints...)
	return i
}

func (i *Interceptor) StreamClientInterceptor() StreamClientInterceptor {
	interceptors := i.chainStreamClientInts
	var chainedInt StreamClientInterceptor
	if len(interceptors) == 0 {
		chainedInt = nil
	} else if len(interceptors) == 1 {
		chainedInt = interceptors[0]
	} else {
		chainedInt = func(ctx context.Context, desc *GRPCStreamDesc, cc *ClientConn, method string, streamer Streamer, opts ...CallOption) (ClientStream, error) {
			return interceptors[0](ctx, desc, cc, method, getChainStreamer(interceptors, 0, streamer), opts...)
		}
	}
	return chainedInt
}

// getChainStreamer recursively generate the chained client stream constructor.
func getChainStreamer(interceptors []StreamClientInterceptor, curr int, finalStreamer Streamer) Streamer {
	if curr == len(interceptors)-1 {
		return finalStreamer
	}
	return func(ctx context.Context, desc *GRPCStreamDesc, cc *ClientConn, method string, opts ...CallOption) (ClientStream, error) {
		return interceptors[curr+1](ctx, desc, cc, method, getChainStreamer(interceptors, curr+1, finalStreamer), opts...)
	}
}
