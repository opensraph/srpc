# srpc
A lightweight RPC framework implementing the Connect RPC protocol with gRPC-compatible interfaces.


## Features

- **Protocol Compatibility**: Implements the Connect RPC protocol to support browser and gRPC-compatible HTTP APIs
- **gRPC Compatible Interface**: Provides the same API experience as gRPC for seamless transitions
- **Interface Compatibility**: Offers interfaces compatible with both standard library and gRPC code
- **Lightweight Design**: Focuses on core functionality without unnecessary complexity
- **Full Streaming Support**: Supports unary calls, server streaming, client streaming, and bidirectional streaming RPCs

## Quick Start

### Installation

```bash
go get github.com/opensraph/srpc
```

### Define Services

Define your services using standard Protocol Buffers:

```protobuf
syntax = "proto3";

package srpc.eliza.v1;

service ElizaService {
  // Unary call
  rpc Say(SayRequest) returns (SayResponse);
  // Bidirectional streaming
  rpc Converse(stream ConverseRequest) returns (stream ConverseResponse);
  // Server streaming
  rpc Introduce(IntroduceRequest) returns (stream IntroduceResponse);
}

message SayRequest {
  string sentence = 1;
}

message SayResponse {
  string sentence = 1;
}

message ConverseRequest {
  string sentence = 1;
}

message ConverseResponse {
  string sentence = 1;
}

message IntroduceRequest {
  string name = 1;
}

message IntroduceResponse {
  string sentence = 1;
}
```

### Server Implementation

SRPC's server API is fully compatible with gRPC, allowing seamless migration of existing gRPC services:

```go
package main

import (
    "context"
    "net"
    "io"
    "errors"
    
    "github.com/opensraph/srpc"
    elizav1 "path/to/generated/proto"
)

type elizaImpl struct {
    elizav1.UnimplementedElizaServiceServer
}

// Implement unary call
func (e elizaImpl) Say(_ context.Context, req *elizav1.SayRequest) (*elizav1.SayResponse, error) {
    return &elizav1.SayResponse{Sentence: "Tell me more about that."}, nil
}

// Implement bidirectional streaming
func (e elizaImpl) Converse(server elizav1.ElizaService_ConverseServer) error {
    for {
        _, err := server.Recv()
        if errors.Is(err, io.EOF) {
            return nil
        }
        if err := server.Send(&elizav1.ConverseResponse{
            Sentence: "Fascinating. Tell me more.",
        }); err != nil {
            return err
        }
    }
}

// Implement server streaming
func (e elizaImpl) Introduce(_ *elizav1.IntroduceRequest, server elizav1.ElizaService_IntroduceServer) error {
    if err := server.Send(&elizav1.IntroduceResponse{
        Sentence: "Hello",
    }); err != nil {
        return err
    }
    return server.Send(&elizav1.IntroduceResponse{
        Sentence: "How are you today?",
    })
}

func main() {
    // Create SRPC server with gRPC-compatible API
    srv := srpc.NewServer()
    
    // Register service implementation using the same API as gRPC
    elizav1.RegisterElizaServiceServer(srv, &elizaImpl{})
    
    // Use standard library net package listener
    lis, err := net.Listen("tcp", ":8080")
    if err != nil {
        panic(err)
    }
    if err := srv.Serve(lis); err != nil {
        panic(err)
    }
}
```

### Client Usage

The client API is also gRPC-compatible while supporting standard library style calling patterns:

```go
package main

import (
    "context"
    "fmt"
    "io"
    "log"
    
    "github.com/opensraph/srpc"
    elizav1 "path/to/generated/proto"
)

func main() {
    // Create client connection with gRPC-compatible API
    conn := srpc.NewClient("localhost:8080")
    client := elizav1.NewElizaServiceClient(conn)
    
    // Unary call
    resp, err := client.Say(context.Background(), &elizav1.SayRequest{
        Sentence: "Hello, I'm feeling sad today.",
    })
    if err != nil {
        log.Fatalf("Failed to call Say: %v", err)
    }
    fmt.Println("Response:", resp.GetSentence())
    
    // Server streaming call
    stream, err := client.Introduce(
        context.Background(),
        &elizav1.IntroduceRequest{Name: "Alice"},
    )
    if err != nil {
        log.Fatalf("Failed to start stream: %v", err)
    }
    for {
        resp, err := stream.Recv()
        if err == io.EOF {
            break
        }
        if err != nil {
            log.Fatalf("Error receiving: %v", err)
        }
        fmt.Println("Received:", resp.GetSentence())
    }
}
```

## Standard Library and gRPC Compatibility Design

SRPC carefully designs interfaces compatible with both Go standard library and gRPC:

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

## License

This project is licensed under the Apache License 2.0 - see the LICENSE file for details.