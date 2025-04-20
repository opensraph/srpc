package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

var (
	serverAddr  = flag.String("server_addr", "https://localhost:50051", "The server address in the format of scheme://host:port")
	authToken   = flag.String("auth_token", "some-secret-token", "Authorization token")
	serviceName = "srpc.examples.echo.Echo"
)

type EchoRequest struct {
	Message string `json:"message"`
}

type EchoResponse struct {
	Message string `json:"message"`
}

func unaryCall(client *http.Client, message string) {
	url := fmt.Sprintf("%s/%s/UnaryEcho", *serverAddr, serviceName)
	reqBody, _ := json.Marshal(EchoRequest{Message: message})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+*authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Unary call failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Unary call returned status: %v", resp.Status)
	}

	var echoResp EchoResponse
	if err := json.NewDecoder(resp.Body).Decode(&echoResp); err != nil {
		log.Fatalf("Failed to decode response: %v", err)
	}
	fmt.Printf("Unary response: %s\n", echoResp.Message)
}

func bidiStreamingCall(client *http.Client, messages []string) {
	url := fmt.Sprintf("%s/%s/BidirectionalStreamingEcho", *serverAddr, serviceName)

	// Create pipe for streaming data
	pr, pw := io.Pipe()

	// Send messages in a goroutine
	go func() {
		defer pw.Close()
		encoder := json.NewEncoder(pw)
		for _, msg := range messages {
			req := EchoRequest{Message: msg}
			if err := encoder.Encode(req); err != nil {
				log.Printf("Failed to send message: %v", err)
				return
			}
			fmt.Printf("Sending message: %s\n", msg)
			time.Sleep(500 * time.Millisecond) // Simulate interval between sends
		}
	}()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, pr)
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+*authToken)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Accept", "application/connect+json")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Bidirectional stream call failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Bidirectional stream call returned status: %v, content: %s", resp.Status, string(body))
	}

	// Read streaming responses
	decoder := json.NewDecoder(resp.Body)
	for {
		var echoResp EchoResponse
		if err := decoder.Decode(&echoResp); err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Failed to parse response: %v", err)
			break
		}
		fmt.Printf("Received response: %s\n", echoResp.Message)
	}
}

func main() {
	flag.Parse()

	// Create HTTP/2 client
	client := &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Perform Unary call
	unaryCall(client, "Hello, Unary HTTP/2!")

	// Perform Bidirectional Streaming call
	bidiStreamingCall(client, []string{"Hello", "from", "Bidirectional", "Streaming", "HTTP/2!"})
}
