package srpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"reflect"
	"runtime"
	"sync"

	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/srpcsync"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/net/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var (
	ErrServerStopped = errors.New("srpc: the server has been stopped")
)

type Server interface {
	grpc.ServiceRegistrar
	reflection.GRPCServer
	Serve(l net.Listener) error
	Stop()
	GracefulStop()
	Handle(pattern string, handler http.Handler)
}

var _ Server = (*server)(nil)

type server struct {
	opts     serverOptions
	mux      *http.ServeMux
	srv      *http.Server
	lis      map[net.Listener]bool
	serve    bool
	services map[string]*serviceDescriptor // service name -> service info
	mu       sync.Mutex
	events   trace.EventLog

	quit    *srpcsync.Event
	done    *srpcsync.Event
	serveWG sync.WaitGroup
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
		lis:      make(map[net.Listener]bool),
		opts:     opts,
		mux:      mux,
		srv:      http1Server,
		services: make(map[string]*serviceDescriptor),
		quit:     srpcsync.NewEvent(),
		done:     srpcsync.NewEvent(),
	}

	if s.opts.enableTracing {
		s.setupTracing()
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

	for _, srv := range s.services {
		procedure := "/" + srv.serviceName + "/"
		s.Handle(procedure, srv.NewHandler())
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

func (s *server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
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
	s.printf("RegisterService(%q)", sd.ServiceName)
	if s.serve {
		s.errorf("srpc: Server.RegisterService called after Server.Serve for %q", sd.ServiceName)
		os.Exit(1)
	}
	if _, ok := s.services[sd.ServiceName]; ok {
		s.errorf("srpc: Server.RegisterService found duplicate service registration for %q", sd.ServiceName)
		os.Exit(1)
	}

	serviceDesc, err := newServiceDescriptor(sd, ss, s.opts)
	if err != nil {
		s.errorf("srpc: Server.RegisterService failed to create service descriptor for %q: %v", sd.ServiceName, err)
		os.Exit(1)
	}

	s.services[sd.ServiceName] = serviceDesc
}

// GetServiceInfo implements Server.
func (s *server) GetServiceInfo() map[string]grpc.ServiceInfo {
	ret := make(map[string]grpc.ServiceInfo)
	for n, srv := range s.services {
		methods := srv.schema.Methods()
		if methods.Len() == 0 {
			continue
		}
		gms := make([]grpc.MethodInfo, len(srv.handlerDesc))
		for i := 0; i < methods.Len(); i++ {
			method := methods.Get(i)
			gms[i] = grpc.MethodInfo{
				Name:           string(method.Name()),
				IsClientStream: method.IsStreamingClient(),
				IsServerStream: method.IsStreamingServer(),
			}
		}

		ret[n] = grpc.ServiceInfo{
			Methods:  gms,
			Metadata: srv.metadata,
		}
	}
	return ret
}

func (s *server) setupTracing() {
	prefix := "/debug"

	_, file, line, _ := runtime.Caller(1)
	s.events = trace.NewEventLog("srpc.Server", fmt.Sprintf("%s:%d", file, line))
	s.mux.HandleFunc(prefix+"/event", trace.Events)
	s.mux.HandleFunc(prefix+"/trace", trace.Traces)
	s.opts.interceptor.ChainUnaryInterceptor(traceUnaryInterceptor())
	s.opts.interceptor.ChainStreamInterceptor(traceStreamInterceptor())

	// setup pprof
	s.mux.HandleFunc(prefix+"/pprof/", pprof.Index)
	s.mux.HandleFunc(prefix+"/pprof/cmdline", pprof.Cmdline)
	s.mux.HandleFunc(prefix+"/pprof/profile", pprof.Profile)
	s.mux.HandleFunc(prefix+"/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc(prefix+"/pprof/trace", pprof.Trace)
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
