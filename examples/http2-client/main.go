package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/opensraph/srpc/examples/proto/gen/srpc/echo/v1"
)

var (
	serverAddr  = flag.String("server_addr", "https://localhost:50051", "The server address in the format of scheme://host:port")
	authToken   = flag.String("auth_token", "some-secret-token", "Authorization token")
	serviceName = "srpc.echo.v1.Echo"
)

// HTTP/2 client
var client *http.Client

// Initialize HTTP/2 client
func init() {
	client = &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Note: Don't skip certificate validation in production
			},
		},
	}
}

// Execute unary call
func unaryCall(message string) {
	fmt.Println("--- Executing Unary Call ---")

	// Build request body
	reqBody, err := protojson.Marshal(&pb.EchoRequest{Message: message})
	if err != nil {
		log.Fatalf("Failed to encode request: %v", err)
	}

	// Create HTTP request
	url := fmt.Sprintf("%s/%s/UnaryEcho", *serverAddr, serviceName)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}

	// Set request headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*authToken)

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response: %v", err)
	}

	// Check status code
	if resp.StatusCode != 200 {
		log.Fatalf("Request failed, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var response pb.EchoResponse
	if err := protojson.Unmarshal(body, &response); err != nil {
		log.Fatalf("Failed to parse response: %v", err)
	}

	fmt.Printf("Unary call response: %s\n", response.Message)
}

// Execute bidirectional streaming call
// TODO: Now it is cannot use.
func bidiStreamingCall(messages []string) {
	fmt.Println("--- Executing Bidirectional Streaming Call ---")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create HTTP request
	url := fmt.Sprintf("%s/%s/BidirectionalStreamingEcho", *serverAddr, serviceName)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil) // Initialize as nil, we will use ReadWriter
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}

	// Set request headers
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Authorization", "Bearer "+*authToken)
	req.Header.Set("Accept", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")

	// Create bidirectional pipe
	pr, pw := io.Pipe()
	req.Body = pr

	// Start a goroutine to send messages
	go func() {
		defer pw.Close()

		// Send messages
		for _, msg := range messages {
			// Create message
			reqBody, err := protojson.Marshal(&pb.EchoRequest{Message: msg})
			if err != nil {
				log.Printf("Failed to encode request: %v", err)
				return
			}

			// Write message directly without length prefix
			if _, err := pw.Write(reqBody); err != nil {
				log.Printf("Failed to write request: %v", err)
				return
			}

			fmt.Printf("Sent message: %s\n", msg)
			time.Sleep(500 * time.Millisecond) // Simulate interval between sends
		}
	}()

	// Send request and get response
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Request failed, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	unmarshaller := protojson.UnmarshalOptions{}

	for {
		buf := bytes.NewBuffer(make([]byte, 0))
		_, err := buf.ReadFrom(resp.Body)
		if err != nil {
			log.Printf("Failed to read response: %v", err)
			break
		}

		var response pb.EchoResponse
		// Read message

		if err := unmarshaller.Unmarshal(buf.Bytes(), &response); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			log.Printf("Failed to decode response: %v", err)
			break
		}

		fmt.Printf("Received response: %s\n", response.Message)
		time.Sleep(500 * time.Millisecond) // Simulate interval between receives
	}
}

func main() {
	flag.Parse()

	// Ensure serverAddr doesn't end with /
	*serverAddr = strings.TrimSuffix(*serverAddr, "/")

	// Execute unary call
	unaryCall("Hello from HTTP/2 client!")

	// Execute bidirectional streaming call
	bidiStreamingCall([]string{
		"Stream message 1",
		"Stream message 2",
		"Stream message 3",
		"Stream message 4",
		"Stream message 5",
	})
}
