package srpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"sync"

	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/srpcsync"
	"github.com/opensraph/srpc/protocol"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/net/trace"
	"google.golang.org/grpc"
)

var (
	EnableTracing    bool = true
	ErrServerStopped      = errors.New("srpc: the server has been stopped")
)

type Server interface {
	grpc.ServiceRegistrar
	Serve(l net.Listener) error
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
	events  trace.EventLog

	quit    *srpcsync.Event
	done    *srpcsync.Event
	serveWG sync.WaitGroup

	serverWorkerChannel      chan func()
	serverWorkerChannelClose func()
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
	http2Server := &http2.Server{
		MaxConcurrentStreams: opts.maxConcurrentStreams,
	}

	http1Server := &http.Server{
		Handler:      h2c.NewHandler(mux, http2Server),
		ReadTimeout:  opts.readTimeout,
		WriteTimeout: opts.writeTimeout,
		IdleTimeout:  opts.idleTimeout,
	}

	s := &server{
		lis:     make(map[net.Listener]bool),
		opts:    opts,
		mux:     mux,
		srv:     http1Server,
		streams: make(map[string]StreamDesc),
		quit:    srpcsync.NewEvent(),
		done:    srpcsync.NewEvent(),
	}

	if EnableTracing {
		_, file, line, _ := runtime.Caller(1)
		s.events = trace.NewEventLog("srpc.Server", fmt.Sprintf("%s:%d", file, line))
		s.mux.Handle("/debug/event", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			trace.RenderEvents(w, r, true)
		}))

	}

	if s.opts.numServerWorkers > 0 {
		s.initServerWorkers()
	}

	return s
}

// Serve implements Server.
func (s *server) Serve(lis net.Listener) error {
	s.mu.Lock()
	s.printf("serving")
	s.serve = true
	if s.lis == nil {
		// Serve called after Stop or GracefulStop.
		s.mu.Unlock()
		lis.Close()
		return ErrServerStopped
	}

	for procedure, desc := range s.streams {
		handler := s.newHandler(desc)
		s.Handle(procedure, handler)
	}

	s.serveWG.Add(1)
	defer func() {
		s.serveWG.Done()
		if s.quit.HasFired() {
			// Stop or GracefulStop called; block until done and return nil.
			<-s.done.Done()
		}
	}()

	ls := &listener{
		Listener: lis,
		creds:    s.opts.creds,
	}

	s.lis[ls] = true
	defer func() {
		s.mu.Lock()
		if s.lis != nil && s.lis[ls] {
			ls.Close()
			delete(s.lis, ls)
		}
		s.mu.Unlock()
	}()

	s.mu.Unlock()

	for {
		if err := s.srv.Serve(ls); err != nil {
			s.errorf("error while listening to https server, err: %v", err)
			if s.quit.HasFired() {
				return nil
			}
		}
	}
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

func (s *server) stop(graceful bool) {
	s.quit.Fire()
	defer s.done.Fire()

	s.mu.Lock()
	s.closeListenersLocked()
	// Wait for serving threads to be ready to exit.  Only then can we be sure no
	// new conns will be created.
	s.mu.Unlock()
	s.serveWG.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	if graceful {
		s.printf("graceful stop")
		s.srv.Shutdown(context.Background())
	} else {
		s.printf("stop")
		s.srv.Close()
	}

	if s.opts.numServerWorkers > 0 {
		// Closing the channel (only once, via sync.OnceFunc) after all the
		// connections have been closed above ensures that there are no
		// goroutines executing the callback passed to st.HandleStreams (where
		// the channel is written to).
		s.serverWorkerChannelClose()
	}

	if s.events != nil {
		s.events.Finish()
		s.events = nil
	}
}

// s.mu must be held by the caller.
func (s *server) closeListenersLocked() {
	for lis := range s.lis {
		lis.Close()
	}
	s.lis = nil
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
			ServiceName:      sd.ServiceName,
			MethodName:       d.MethodName,
			StreamType:       StreamTypeUnary,
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
			ServiceName:      sd.ServiceName,
			MethodName:       d.StreamName,
			ServiceImpl:      ss,
			StreamType:       parseGrpcStreamType(d),
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

func (s *server) unaryImplementation(desc StreamDesc, handlerOpts []HandlerOption) Implementation {
	return func(ctx context.Context, coon protocol.StreamingHandlerConn) error {
		handler, ok := desc.Handler.(grpc.MethodHandler)
		if !ok {
			panic(errors.Newf("invalid unary handler type: %T", desc.Handler))
		}
		response, err := handler(desc.ServiceImpl, ctx, coon.Receive, s.opts.interceptor.UnaryInterceptor())
		if err != nil {
			return err
		}
		return coon.Send(response)
	}
}

func (s *server) streamImplementation(desc StreamDesc, handlerOpts []HandlerOption) Implementation {
	return func(ctx context.Context, conn protocol.StreamingHandlerConn) error {
		handler, ok := desc.Handler.(grpc.StreamHandler)
		if !ok {
			panic(errors.Newf("invalid stream handler type: %T", desc.Handler))
		}
		if interceptor := s.opts.interceptor.StreamInterceptor(); interceptor != nil {
			err := interceptor(desc.ServiceImpl, newGRPCServerStreamBridge(ctx, conn), &grpc.StreamServerInfo{
				FullMethod:     fmt.Sprintf("/%s/%s", desc.ServiceName, desc.MethodName),
				IsClientStream: desc.StreamType.IsClient(),
				IsServerStream: desc.StreamType.IsServer(),
			}, handler)
			if err != nil {
				return err
			}
		}

		return handler(desc.ServiceImpl, newGRPCServerStreamBridge(ctx, conn))
	}
}

// printf records an event in s's event log, unless s has been stopped.
// REQUIRES s.mu is held.
func (s *server) printf(format string, a ...any) {
	if s.events != nil {
		s.events.Printf(format, a...)
	}
}

// errorf records an error in s's event log, unless s has been stopped.
// REQUIRES s.mu is held.
func (s *server) errorf(format string, a ...any) {
	if s.events != nil {
		s.events.Errorf(format, a...)
	}
}

// serverWorkerResetThreshold defines how often the stack must be reset. Every
// N requests, by spawning a new goroutine in its place, a worker can reset its
// stack so that large stacks don't live in memory forever. 2^16 should allow
// each goroutine stack to live for at least a few seconds in a typical
// workload (assuming a QPS of a few thousand requests/sec).
const serverWorkerResetThreshold = 1 << 16

// serverWorker blocks on a *transport.ServerStream channel forever and waits
// for data to be fed by serveStreams. This allows multiple requests to be
// processed by the same goroutine, removing the need for expensive stack
// re-allocations (see the runtime.morestack problem [1]).
//
// [1] https://github.com/golang/go/issues/18138
func (s *server) serverWorker() {
	for completed := 0; completed < serverWorkerResetThreshold; completed++ {
		f, ok := <-s.serverWorkerChannel
		if !ok {
			return
		}
		f()
	}
	go s.serverWorker()
}

// initServerWorkers creates worker goroutines and a channel to process incoming
// connections to reduce the time spent overall on runtime.morestack.
func (s *server) initServerWorkers() {
	s.serverWorkerChannel = make(chan func())
	s.serverWorkerChannelClose = sync.OnceFunc(func() {
		close(s.serverWorkerChannel)
	})
	for i := uint32(0); i < s.opts.numServerWorkers; i++ {
		go s.serverWorker()
	}
}
