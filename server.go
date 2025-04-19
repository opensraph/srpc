package srpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sync"

	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/protocol"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

type Server interface {
	grpc.ServiceRegistrar
	Serve(l net.Listener) error
	ServeTLS(l net.Listener, certFile string, keyFile string) error
	Stop()
	GracefulStop()
	Handle(pattern string, handler http.Handler)
}

var _ Server = (*server)(nil)

type server struct {
	opts    serverOptions
	mux     *http.ServeMux
	srv     *http.Server
	lis     map[net.Listener]bool
	serve   bool
	streams map[string]StreamDesc
	mu      sync.Mutex
}

func NewServer(opt ...ServerOption) *server {
	opts := defaultServerOptions
	for _, o := range globalServerOptions {
		o(&opts)
	}
	for _, o := range opt {
		o(&opts)
	}
	mux := http.NewServeMux()
	http2Server := &http2.Server{}
	http1Server := &http.Server{
		Handler:      h2c.NewHandler(mux, http2Server),
		ReadTimeout:  opts.readTimeout,
		WriteTimeout: opts.writeTimeout,
		IdleTimeout:  opts.idleTimeout,
	}

	return &server{
		lis:     make(map[net.Listener]bool),
		opts:    opts,
		mux:     mux,
		srv:     http1Server,
		serve:   false,
		streams: make(map[string]StreamDesc),
	}
}

// Serve implements Server.
func (s *server) Serve(l net.Listener) error {
	if err := s.run(); err != nil {
		return err
	}
	return s.srv.Serve(l)
}

// ServeTLS implements Server.
func (s *server) ServeTLS(l net.Listener, certFile string, keyFile string) error {
	if err := s.run(); err != nil {
		return err
	}
	return s.srv.ServeTLS(l, certFile, keyFile)
}

// Stop stops the sRPC server. It immediately closes all open
// connections and listeners.
// It cancels all active RPCs on the server side and the corresponding
// pending RPCs on the client side will get notified by connection
// errors.
func (s *server) Stop() {
	s.stop(false)
}

// GracefulStop stops the sRPC server gracefully. It stops the server from
// accepting new connections and RPCs and blocks until all the pending RPCs are
// finished.
func (s *server) GracefulStop() {
	s.stop(true)
}

func (s *server) run() error {
	if s.srv == nil {
		return fmt.Errorf("server is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serve = true
	for procedure, desc := range s.streams {
		handler := s.newHandler(desc)
		s.Handle(procedure, handler)
	}
	return nil
}

func (s *server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// RegisterService implements grpc.ServiceRegistrar.
func (s *server) RegisterService(sd *grpc.ServiceDesc, ss any) {
	if ss != nil {
		ht := reflect.TypeOf(sd.HandlerType).Elem()
		st := reflect.TypeOf(ss)
		if !st.Implements(ht) {

			panic(fmt.Errorf("RegisterService type mismatch: %s does not implement %s", st, ht))
		}
	}
	s.register(sd, ss)
}

func (s *server) register(sd *grpc.ServiceDesc, ss any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.printf("RegisterService: %s", sd.ServiceName)
	if s.serve {
		panic(fmt.Errorf("RegisterService after Serve: %s", sd.ServiceName))
	}
	for i := range sd.Methods {
		d := &sd.Methods[i]
		procedure := s.checkAndGetProcedure(sd.ServiceName, d.MethodName)
		desc := StreamDesc{
			ServiceImpl:      ss,
			StreamType:       StreamTypeUnary,
			Procedure:        procedure,
			Handler:          d.Handler,
			IsClient:         false,
			IdempotencyLevel: IdempotencyNoSideEffects,
		}
		s.streams[procedure] = desc
	}

	for i := range sd.Streams {
		d := &sd.Streams[i]
		procedure := s.checkAndGetProcedure(sd.ServiceName, d.StreamName)
		desc := StreamDesc{
			ServiceImpl:      ss,
			StreamType:       parseGrpcStreamType(d),
			Procedure:        procedure,
			Handler:          d.Handler,
			IsClient:         false,
			IdempotencyLevel: IdempotencyUnknown,
		}
		s.streams[procedure] = desc
	}
}

func (s *server) checkAndGetProcedure(serviceName, methodName string) string {
	procedure := fmt.Sprintf("/%s/%s", serviceName, methodName)
	if _, ok := s.streams[procedure]; ok {
		panic(fmt.Errorf("RegisterService duplicate procedure: %s", procedure))
	}
	return procedure
}

func (s *server) newHandler(desc StreamDesc, handlerOpts ...HandlerOption) *Handler {
	o := newHandlerOption(desc, s.opts, handlerOpts)
	handlers := o.newProtocolHandlers()
	methodHandlers := mappedMethodHandlers(handlers)
	allowMethodValue := sortedAllowMethodValue(handlers)
	acceptPostValue := sortedAcceptPostValue(handlers)

	implementation := s.newImplementation(desc, handlerOpts)

	return &Handler{
		ctx:  context.Background(),
		opts: o,

		protocolHandlers: methodHandlers,
		allowMethod:      allowMethodValue,
		acceptPost:       acceptPostValue,

		desc:           desc,
		implementation: implementation,
	}
}

type Implementation func(ctx context.Context, stream protocol.StreamingHandlerConn) error

func (s *server) newImplementation(desc StreamDesc, handlerOpts []HandlerOption) Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		switch desc.StreamType {
		case StreamTypeUnary:
			return s.unaryImplementation(desc, handlerOpts)(ctx, conn)
		case StreamTypeClient, StreamTypeServer, StreamTypeBidi:
			return s.streamImplementation(desc, handlerOpts)(ctx, conn)
		default:
			panic(errors.Newf("invalid stream type: %v", desc.StreamType))
		}
	}
}

func (s *server) streamImplementation(desc StreamDesc, handlerOpts []HandlerOption) Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		handler, ok := desc.Handler.(grpc.StreamHandler)
		if !ok {
			panic(errors.Newf("invalid stream handler type: %T", desc.Handler))
		}
		if interceptor := s.opts.interceptor; interceptor != nil {
			handler = interceptor.WrapStreamingHandler(handler)
		}
		return handler(desc.ServiceImpl, newGRPCServerStreamBridge(ctx, conn))
	}
}

func (s *server) unaryImplementation(desc StreamDesc, handlerOpts []HandlerOption) Implementation {
	return func(ctx context.Context, stream protocol.StreamingHandlerConn) error {
		handler, ok := desc.Handler.(grpc.MethodHandler)
		if !ok {
			panic(errors.Newf("invalid unary handler type: %T", desc.Handler))
		}

		var grpcUnaryServerInterceptor grpc.UnaryServerInterceptor
		if interceptor := s.opts.interceptor; interceptor != nil {
			grpcUnaryServerInterceptor = func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
				return interceptor.WrapUnary(func(ctx context.Context, req any) (any, error) {
					return handler(ctx, req)
				})(ctx, req)
			}
		}

		response, err := handler(desc.ServiceImpl, ctx, stream.Receive, grpcUnaryServerInterceptor)
		if err != nil {
			return err
		}
		return stream.Send(response)
	}
}

func (s *server) stop(graceful bool) {
	s.mu.Lock()
	s.closeListenersLocked()
	// Wait for serving threads to be ready to exit.  Only then can we be sure no
	// new conns will be created.
	s.mu.Unlock()

	if graceful {

	} else {
	}
}

// s.mu must be held by the caller.
func (s *server) closeListenersLocked() {
	for lis := range s.lis {
		lis.Close()
	}
	s.lis = nil
}

// printf records an event in s's event log, unless s has been stopped.
// REQUIRES s.mu is held.
func (s *server) printf(format string, a ...any) {

}

// errorf records an error in s's event log, unless s has been stopped.
// REQUIRES s.mu is held.
func (s *server) errorf(format string, a ...any) {

}
