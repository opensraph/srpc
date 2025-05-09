package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/opensraph/srpc"
	certs "github.com/opensraph/srpc/examples/_certs"
	ecpb "github.com/opensraph/srpc/examples/_proto/go/srpc/echo/v1"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
)

var addr = flag.String("addr", "localhost:50051", "the address to connect to")

const fallbackToken = "some-secret-token"

// logger is to mock a sophisticated logging system. To simplify the example, we just print out the content.
func logger(format string, a ...any) {
	fmt.Printf("LOG:\t"+format+"\n", a...)
}

// unaryInterceptor is an example unary interceptor.
func unaryInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	var credsConfigured bool
	for _, o := range opts {
		_, ok := o.(grpc.PerRPCCredsCallOption)
		if ok {
			credsConfigured = true
			break
		}
	}
	if !credsConfigured {
		opts = append(opts, grpc.PerRPCCredentials(oauth.TokenSource{
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: fallbackToken}),
		}))
	}
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	end := time.Now()
	logger("RPC: %s, start time: %s, end time: %s, err: %v", method, start.Format("Basic"), end.Format(time.RFC3339), err)
	return err
}

// wrappedStream  wraps around the embedded grpc.ClientStream, and intercepts the RecvMsg and
// SendMsg method call.
type wrappedStream struct {
	grpc.ClientStream
}

func (w *wrappedStream) RecvMsg(m any) error {
	logger("Receive a message (Type: %T) at %v", m, time.Now().Format(time.RFC3339))
	return w.ClientStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m any) error {
	logger("Send a message (Type: %T) at %v", m, time.Now().Format(time.RFC3339))
	return w.ClientStream.SendMsg(m)
}

func newWrappedStream(s grpc.ClientStream) grpc.ClientStream {
	return &wrappedStream{s}
}

// streamInterceptor is an example stream interceptor.
func streamInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	var credsConfigured bool
	for _, o := range opts {
		_, ok := o.(*grpc.PerRPCCredsCallOption)
		if ok {
			credsConfigured = true
			break
		}
	}
	if !credsConfigured {
		opts = append(opts, grpc.PerRPCCredentials(oauth.TokenSource{
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: fallbackToken}),
		}))
	}
	s, err := streamer(ctx, desc, cc, method, opts...)
	if err != nil {
		return nil, err
	}
	return newWrappedStream(s), nil
}

func callUnaryEcho(client ecpb.EchoServiceClient, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.UnaryEcho(ctx, &ecpb.EchoRequest{Message: message})
	if err != nil {
		log.Fatalf("client.UnaryEcho(_) = _, %v: ", err)
	}
	fmt.Println("UnaryEcho: ", resp.Message)
}

func callBidiStreamingEcho(client ecpb.EchoServiceClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.BidirectionalStreamingEcho(ctx)
	if err != nil {
		return
	}

	// Send messages in a goroutine
	go func() {
		for i := 0; i < 5; i++ {
			if err := c.Send(&ecpb.EchoRequest{Message: fmt.Sprintf("Request %d", i+1)}); err != nil {
				log.Fatalf("failed to send request due to error: %v", err)
			}
			time.Sleep(500 * time.Millisecond) // Simulate interval between sends
		}
		// Close sending side when done
		if err := c.CloseSend(); err != nil {
			log.Fatalf("failed to close send stream due to error: %v", err)
		}
	}()
	for {
		resp, err := c.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("failed to receive response due to error: %v", err)
		}
		fmt.Println("BidiStreaming Echo: ", resp.Message)
	}
}

func main() {
	flag.Parse()

	// Create tls based credential.
	creds, err := credentials.NewClientTLSFromFile(certs.Path("localhost+2.pem"), "localhost")
	if err != nil {
		log.Fatalf("failed to load credentials: %v", err)
	}

	// Set up a connection to the server.
	conn, err := srpc.NewClient(*addr,
		srpc.WithGRPCOptions(
			grpc.WithTransportCredentials(creds),
		),
		srpc.WithUnaryInterceptor(unaryInterceptor),
		srpc.WithStreamInterceptor(streamInterceptor),
	)
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	// Make an echo client and send RPCs.
	rgc := ecpb.NewEchoServiceClient(conn)
	callUnaryEcho(rgc, "hello world")
	callBidiStreamingEcho(rgc)
}
