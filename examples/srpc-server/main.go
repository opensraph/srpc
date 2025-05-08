package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/opensraph/srpc"
	"github.com/opensraph/srpc/errors"
	"github.com/rs/cors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"

	certs "github.com/opensraph/srpc/examples/_certs"
	pb "github.com/opensraph/srpc/examples/_proto/go/srpc/echo/v1"
)

var (
	port = flag.Int("port", 50051, "the port to serve on")

	errMissingMetadata = errors.Newf("missing metadata").WithCode(errors.InvalidArgument)
	errInvalidToken    = errors.Newf("invalid token").WithCode(errors.Unauthenticated)
)

// logger is to mock a sophisticated logging system. To simplify the example, we just print out the content.
func logger(format string, a ...any) {
	fmt.Printf("LOG:\t"+format+"\n", a...)
}

type server struct {
	pb.UnimplementedEchoServiceServer
}

func (s *server) UnaryEcho(_ context.Context, in *pb.EchoRequest) (*pb.EchoResponse, error) {
	logger("unary echoing message %q\n", in.Message)
	return &pb.EchoResponse{Message: in.Message}, nil
}

func (s *server) ClientStreamingEcho(stream pb.EchoService_ClientStreamingEchoServer) error {
	var message string
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			// We have finished reading the stream.
			break
		}
		if err != nil {
			logger("server: error receiving from stream: %v\n", err)
		}
		logger("client streaming echoing message %q\n", in.Message)
		message += in.Message
	}
	if err := stream.SendAndClose(&pb.EchoResponse{Message: message}); err != nil {
		logger("server: error sending to stream: %v\n", err)
	}
	logger("client streaming echoing message %q\n", message)
	return nil
}

func (s *server) ServerStreamingEcho(in *pb.EchoRequest, stream pb.EchoService_ServerStreamingEchoServer) error {
	logger("server streaming echoing message %q\n", in.Message)
	for i := 0; i < 5; i++ {
		if err := stream.Send(&pb.EchoResponse{Message: in.Message}); err != nil {
			logger("server: error sending to stream: %v\n", err)
			return err
		}
		time.Sleep(1 * time.Second)
	}
	return nil
}

func (s *server) BidirectionalStreamingEcho(stream pb.EchoService_BidirectionalStreamingEchoServer) error {
	for {
		in, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			logger("server: error receiving from stream: %v\n", err)
			return err
		}
		logger("bidi echoing message %q\n", in.Message)
		stream.Send(&pb.EchoResponse{Message: in.Message})
	}
}

// valid validates the authorization.
func valid(authorization []string) bool {
	if len(authorization) < 1 {
		return false
	}
	token := strings.TrimPrefix(authorization[0], "Bearer ")
	// Perform the token validation here. For the sake of this example, the code
	// here forgoes any of the usual OAuth2 token validation and instead checks
	// for a token matching an arbitrary string.
	return token == "some-secret-token"
}

func unaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	// authentication (token verification)
	md, ok := metadata.FromIncomingContext(ctx)

	if !ok {
		return nil, errMissingMetadata
	}
	if !valid(md["authorization"]) {
		return nil, errInvalidToken
	}
	m, err := handler(ctx, req)
	if err != nil {
		logger("RPC failed with error: %v", err)
	}
	return m, err
}

// wrappedStream wraps around the embedded grpc.ServerStream, and intercepts the RecvMsg and
// SendMsg method call.
type wrappedStream struct {
	grpc.ServerStream
}

func (w *wrappedStream) RecvMsg(m any) error {
	// logger("Receive a message (Type: %T) at %s", m, time.Now().Format(time.RFC3339))
	return w.ServerStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m any) error {
	// logger("Send a message (Type: %T) at %v", m, time.Now().Format(time.RFC3339))
	return w.ServerStream.SendMsg(m)
}

func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	return &wrappedStream{s}
}

func streamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// authentication (token verification)
	md, ok := metadata.FromIncomingContext(ss.Context())
	if !ok {
		return errMissingMetadata
	}
	if !valid(md["authorization"]) {
		return errInvalidToken
	}

	err := handler(srv, newWrappedStream(ss))
	if err != nil {
		logger("RPC failed with error: %v", err)
	}
	return err
}

func main() {
	flag.Parse()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	// Create tls based credential.
	creds, err := credentials.NewServerTLSFromFile(certs.Path("localhost+2.pem"), certs.Path("localhost+2-key.pem"))
	if err != nil {
		log.Fatalf("failed to create credentials: %v", err)
	}

	s := srpc.NewServer(
		srpc.Creds(creds),
		srpc.UnaryInterceptor(unaryInterceptor),
		srpc.StreamInterceptor(streamInterceptor),
		srpc.EnableTracing(),
		srpc.GlobalHandler(corsHandler),
	)

	// Reflection
	reflection.Register(s)

	// Register EchoServer on the server.
	pb.RegisterEchoServiceServer(s, &server{})

	logger("server listening at %v", lis.Addr())

	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func corsHandler(next http.Handler) http.Handler {
	return cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   allowedMethods(),
		AllowedHeaders:   allowedHeaders(),
		ExposedHeaders:   exposedHeaders(),
		MaxAge:           7200,
		AllowCredentials: true,
	}).Handler(next)
}

func allowedMethods() []string {
	return []string{
		"GET",  // for Connect
		"POST", // for all protocols
	}
}

func allowedHeaders() []string {
	return []string{
		// for all protocols
		"Accept",
		"Accept-Encoding",
		"Accept-Language",
		"Authorization",
		"Content-Type",
		"Origin",

		// for Connect
		"Connect-Protocol-Version",
		"Connect-Timeout-Ms",
		"Connect-Binary-Metadata",

		// for gRPC-Web
		"Grpc-Timeout",
		"X-Grpc-Web",
		"X-User-Agent",

		// for HTTP/2
		"Content-Length",
		"Keep-Alive",
		"TE",
		"Trailer",
		"Transfer-Encoding",
	}
}

func exposedHeaders() []string {
	return []string{
		// for grpc-web
		"Grpc-Status",
		"Grpc-Message",
		"Grpc-Status-Details-Bin",

		// for Connect
		"Connect-Protocol-Version",

		// for HTTP/2
		"Content-Encoding",
		"Content-Length",
		"Date",
		"Server",
		"Trailer",

		// server custom headers
		"X-Server-Time",
	}
}
