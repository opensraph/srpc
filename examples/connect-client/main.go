package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/net/http2"

	"connectrpc.com/connect"
	pb "github.com/opensraph/srpc/examples/proto/echo"
	echov1connect "github.com/opensraph/srpc/examples/proto/echo/echoconnect"
)

var (
	serverAddr = flag.String("server_addr", "https://localhost:50051", "The server address")
	authToken  = flag.String("auth_token", "some-secret-token", "Authorization token")
)

func unaryCall(client echov1connect.EchoClient, message string) {
	fmt.Println("--- Executing Unary Call ---")
	req := connect.NewRequest(&pb.EchoRequest{Message: message})
	req.Header().Set("Authorization", "Bearer "+*authToken)

	resp, err := client.UnaryEcho(context.Background(), req)
	if err != nil {
		log.Fatalf("Unary call failed: %v", err)
	}

	fmt.Printf("Unary response: %s\n", resp.Msg.Message)
}

func bidiStreamingCall(client echov1connect.EchoClient, messages []string) {
	fmt.Println("--- Executing Bidirectional Streaming Call ---")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create bidirectional stream
	stream := client.BidirectionalStreamingEcho(ctx)

	// Set authentication header
	stream.RequestHeader().Set("Authorization", "Bearer "+*authToken)

	// Send messages in a goroutine
	go func() {
		for _, msg := range messages {
			if err := stream.Send(&pb.EchoRequest{Message: msg}); err != nil {
				log.Printf("Failed to send message: %v", err)
				return
			}
			fmt.Printf("Sent message: %s\n", msg)
			time.Sleep(500 * time.Millisecond) // Simulate interval between sends
		}
		// Close sending side when done
		stream.CloseRequest()
	}()

	// Receive responses
	for {
		resp, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) || connect.CodeOf(err) == connect.CodeUnavailable {
				break // Stream closed
			}
			log.Printf("Failed to receive response: %v", err)
			break
		}
		fmt.Printf("Received response: %s\n", resp.Message)
	}
}

func main() {
	flag.Parse()

	// Create HTTP client with TLS configuration
	httpClient := &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Note: Do not skip certificate validation in production
			},
		},
	}

	// Create ConnectRPC client
	client := echov1connect.NewEchoClient(
		httpClient,
		*serverAddr,
	)

	// Execute unary call
	unaryCall(client, "Hello, ConnectRPC!")

	// Execute bidirectional streaming call
	bidiStreamingCall(client, []string{
		"Message 1",
		"Message 2",
		"Message 3",
		"Message 4",
		"Message 5",
	})
}
