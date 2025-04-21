package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/opensraph/srpc/errors"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc/examples/data"

	connect "connectrpc.com/connect"

	echov1connect "github.com/opensraph/srpc/examples/proto/gen/srpc/echo/v1/echov1connect"

	pb "github.com/opensraph/srpc/examples/proto/gen/srpc/echo/v1"
)

var (
	port = flag.Int("port", 50051, "the port to serve on")

	errMissingMetadata = errors.Newf("missing metadata").WithCode(errors.InvalidArgument)
	errInvalidToken    = errors.Newf("invalid token").WithCode(errors.Unauthenticated)
)

// logger mocks a sophisticated logging system
func logger(format string, a ...any) {
	fmt.Printf("LOG:\t"+format+"\n", a...)
}

// valid validates the authorization
func valid(authorization string) bool {
	if authorization == "" {
		return false
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	// Perform token validation here. For this example, we just check if the token matches a string
	return token == "some-secret-token"
}

var _ echov1connect.EchoHandler = (*server)(nil)

// Define server struct
type server struct {
	echov1connect.UnimplementedEchoHandler
}

// Implement unary call interface
func (s *server) UnaryEcho(
	ctx context.Context,
	req *connect.Request[pb.EchoRequest],
) (*connect.Response[pb.EchoResponse], error) {
	fmt.Printf("unary echoing message %q\n", req.Msg.Message)
	return connect.NewResponse(&pb.EchoResponse{Message: req.Msg.Message}), nil
}

// Implement bidirectional streaming interface
func (s *server) BidirectionalStreamingEcho(
	ctx context.Context,
	stream *connect.BidiStream[pb.EchoRequest, pb.EchoResponse],
) error {
	for {
		req, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			fmt.Printf("server: error receiving from stream: %v\n", err)
			return err
		}
		fmt.Printf("bidi echoing message %q\n", req.Message)
		if err := stream.Send(&pb.EchoResponse{Message: req.Message}); err != nil {
			return err
		}
	}
}

// Authentication interceptor
func authInterceptor() connect.UnaryInterceptorFunc {
	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			// Validate authorization
			auth := req.Header().Get("Authorization")
			if !valid(auth) {
				return nil, connect.NewError(connect.CodeUnauthenticated, errInvalidToken)
			}

			resp, err := next(ctx, req)
			if err != nil {
				logger("RPC failed with error: %v", err)
			}
			return resp, err
		}
	}
	return interceptor
}

// Logger interceptor
func loggerInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {
			start := time.Now()
			logger("Received request: %s at %s", req.Spec().Procedure, start.Format(time.RFC3339))

			resp, err := next(ctx, req)

			end := time.Now()
			logger("Completed request: %s in %v", req.Spec().Procedure, end.Sub(start))
			return resp, err
		}
	}
}

func main() {
	flag.Parse()

	// Load TLS certificates
	cert, err := tls.LoadX509KeyPair(data.Path("x509/server_cert.pem"), data.Path("x509/server_key.pem"))
	if err != nil {
		log.Fatalf("failed to load key pair: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	// Create Connect server
	echoServer := &server{}

	// Set up interceptor options
	interceptors := connect.WithInterceptors(
		authInterceptor(),
		loggerInterceptor(),
	)

	// Create routes
	mux := http.NewServeMux()
	mux.Handle(echov1connect.NewEchoHandler(echoServer, interceptors))

	// Configure HTTP server
	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:      addr,
		Handler:   h2c.NewHandler(mux, &http2.Server{}),
		TLSConfig: tlsConfig,
	}

	log.Printf("starting connect server on %s", addr)
	if err := httpServer.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
