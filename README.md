# sRPC

English | [中文](README_zh.md)

sRPC is a lightweight RPC framework that implements the Connect RPC protocol with gRPC-compatible interfaces. It seamlessly connects browsers and backend services using unified Protocol Buffer definitions.

[![Go Reference](https://pkg.go.dev/badge/github.com/opensraph/srpc.svg)](https://pkg.go.dev/github.com/opensraph/srpc)
[![Go Report Card](https://goreportcard.com/badge/github.com/opensraph/srpc)](https://goreportcard.com/report/github.com/opensraph/srpc)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

## Overview

sRPC combines the simplicity and browser compatibility of Connect with the robust interface design of gRPC. With a single API definition, you can build services that support both browser clients and native gRPC clients.

## Why sRPC?

Connect is an excellent choice for new projects due to its simplicity and browser compatibility. sRPC, however, focuses on compatibility, offering a seamless way for existing gRPC projects to support HTTP and web clients without significant code changes. With sRPC, you can:

- **Retain gRPC Compatibility**: Continue using your existing gRPC services and interfaces.
- **Expand Client Support**: Enable HTTP and browser-based clients to interact with your services.
- **Minimize Migration Effort**: Avoid rewriting or heavily modifying your existing gRPC code.

## Features

- **Protocol Compatibility**: Implements the Connect RPC protocol, supporting browser and gRPC-compatible HTTP APIs.
- **gRPC-Compatible Interface**: Provides the same API experience as gRPC for seamless transitions.
- **Standard Library Compatibility**: Works with both Go's standard library and gRPC code.
- **Lightweight Design**: Focuses on core functionality without unnecessary complexity.
- **Full Streaming Support**: Supports unary calls, server streaming, client streaming, and bidirectional streaming.
- **Interceptors and Middleware**: Offers a flexible request/response processing pipeline.
- **Error Handling**: Structured error types compatible with Go's standard errors and gRPC status codes.
- **Transport Agnostic**: Supports HTTP/1.1 and HTTP/2.

## Quick Start

### Installation

```bash
go get github.com/opensraph/srpc
```

### Define Services

Define your services using standard Protocol Buffers:

```protobuf
syntax = "proto3";

package srpc.examples.echo;

// EchoRequest represents the echo request.
message EchoRequest {
  string message = 1;
}

// EchoResponse represents the echo response.
message EchoResponse {
  string message = 1;
}

// Echo defines the echo service.
service Echo {
  // UnaryEcho is a unary echo.
  rpc UnaryEcho(EchoRequest) returns (EchoResponse) {}
  // ServerStreamingEcho is server-side streaming.
  rpc ServerStreamingEcho(EchoRequest) returns (stream EchoResponse) {}
  // ClientStreamingEcho is client-side streaming.
  rpc ClientStreamingEcho(stream EchoRequest) returns (EchoResponse) {}
  // BidirectionalStreamingEcho is bidirectional streaming.
  rpc BidirectionalStreamingEcho(stream EchoRequest) returns (stream EchoResponse) {}
}
```

### Server Implementation

sRPC's server API is fully compatible with gRPC, allowing seamless migration of existing gRPC services. Below is an example of a server implementation with authentication and logging interceptors:

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "io"
    "log"
    "net"
    "time"

    "github.com/opensraph/srpc"
    "github.com/opensraph/srpc/errors"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
    "google.golang.org/grpc/examples/data"
    "google.golang.org/grpc/metadata"

    pb "google.golang.org/grpc/examples/features/proto/echo"
)

var (
    port = flag.Int("port", 50051, "the port to serve on")

    errMissingMetadata = errors.Newf("missing metadata").WithCode(errors.InvalidArgument)
    errInvalidToken    = errors.Newf("invalid token").WithCode(errors.Unauthenticated)
)

type server struct {
    pb.UnimplementedEchoServer
}

func (s *server) UnaryEcho(_ context.Context, in *pb.EchoRequest) (*pb.EchoResponse, error) {
    fmt.Printf("unary echoing message %q\n", in.Message)
    return &pb.EchoResponse{Message: in.Message}, nil
}

func (s *server) BidirectionalStreamingEcho(stream pb.Echo_BidirectionalStreamingEchoServer) error {
    for {
        in, err := stream.Recv()
        if err != nil {
            if err == io.EOF {
                return nil
            }
            fmt.Printf("server: error receiving from stream: %v\n", err)
            return err
        }
        fmt.Printf("bidi echoing message %q\n", in.Message)
        stream.Send(&pb.EchoResponse{Message: in.Message})
    }
}

func main() {
    flag.Parse()

    lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
    if err != nil {
        log.Fatalf("failed to listen: %v", err)
    }

    creds, err := credentials.NewServerTLSFromFile(data.Path("x509/server_cert.pem"), data.Path("x509/server_key.pem"))
    if err != nil {
        log.Fatalf("failed to create credentials: %v", err)
    }

    s := srpc.NewServer(
        srpc.Creds(creds),
        srpc.UnaryInterceptor(unaryInterceptor),
        srpc.StreamInterceptor(streamInterceptor),
    )

    pb.RegisterEchoServer(s, &server{})

    if err := s.Serve(lis); err != nil {
        log.Fatalf("failed to serve: %v", err)
    }
}
```

### Client Usage

The client API is also gRPC-compatible while supporting standard library-style calling patterns. Below is an example of a client implementation with logging and token injection interceptors:

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "io"
    "log"
    "time"

    "github.com/opensraph/srpc"
    "golang.org/x/oauth2"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
    "google.golang.org/grpc/credentials/oauth"
    "google.golang.org/grpc/examples/data"
    ecpb "google.golang.org/grpc/examples/features/proto/echo"
)

var addr = flag.String("addr", "localhost:50051", "the address to connect to")

const fallbackToken = "some-secret-token"

func callUnaryEcho(client ecpb.EchoClient, message string) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    resp, err := client.UnaryEcho(ctx, &ecpb.EchoRequest{Message: message})
    if err != nil {
        log.Fatalf("client.UnaryEcho(_) = _, %v: ", err)
    }
    fmt.Println("UnaryEcho: ", resp.Message)
}

func callBidiStreamingEcho(client ecpb.EchoClient) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    c, err := client.BidirectionalStreamingEcho(ctx)
    if err != nil {
        return
    }
    for i := 0; i < 5; i++ {
        if err := c.Send(&ecpb.EchoRequest{Message: fmt.Sprintf("Request %d", i+1)}); err != nil {
            log.Fatalf("failed to send request due to error: %v", err)
        }
    }
    c.CloseSend()
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

    creds, err := credentials.NewClientTLSFromFile(data.Path("x509/ca_cert.pem"), "x.test.example.com")
    if err != nil {
        log.Fatalf("failed to load credentials: %v", err)
    }

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

    client := ecpb.NewEchoClient(conn)

    callUnaryEcho(client, "hello world")
    callBidiStreamingEcho(client)
}
```

### Testing with cURL

You can test the `UnaryEcho` method of the sRPC server using `cURL`. Below is an example:

```bash
curl \
    --insecure \
    --header "Content-Type: application/json" \
    --data '{"message": "Hello sRPC"}' \
    https://localhost:50051/grpc.examples.echo.Echo/UnaryEcho
```

## Compatibility Design

sRPC is carefully designed to be compatible with both Go's standard library and gRPC:

### Standard Library Compatibility

- Error handling uses the standard `error` interface

```go
package main

import (
    "github.com/opensraph/srpc/errors"
    "google.golang.org/protobuf/types/known/anypb"
)

func main() {
    var err *errors.Error
    err = errors.New("an error occurred").WithCode(errors.InvalidArgument)
    err.WithDetails(&anypb.Any{
        TypeUrl: "type.googleapis.com/google.protobuf.StringValue",
        Value:   []byte("value"),
    })
    err.WithDetailFromMap(map[string]any{
        "key": "value",
    })
}
```

### gRPC Compatibility

- Provides the same registration interfaces as `grpc.ServiceRegistrar`
- Uses the same service descriptor structures

## Examples

The repository contains working examples that demonstrate various use cases:

- [Basic Server](examples/srpc-server/main.go): A simple sRPC server implementation with authentication and logging interceptors. Demonstrates unary and bidirectional streaming RPCs, token validation, and TLS-based credentials.
- [Basic Client](examples/srpc-client/main.go): A corresponding client implementation showcasing unary and bidirectional streaming RPC calls, logging and token injection interceptors, and TLS-based secure connections.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for details.
