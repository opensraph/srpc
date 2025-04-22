# sRPC

[English](README.md) | 中文

sRPC 是一个轻量级的 RPC 框架，使用与 gRPC 兼容的接口实现了 Connect RPC 协议。通过统一的 Protocol Buffer 定义，sRPC 实现了浏览器与后端服务的无缝连接。

[![Go Reference](https://pkg.go.dev/badge/github.com/opensraph/srpc.svg)](https://pkg.go.dev/github.com/opensraph/srpc)
[![Go Report Card](https://goreportcard.com/badge/github.com/opensraph/srpc)](https://goreportcard.com/report/github.com/opensraph/srpc)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

## 概述

sRPC 结合了 Connect 的简单易用性和浏览器兼容性，以及 gRPC 的强大接口设计。通过一个 API 定义，您可以构建同时支持浏览器客户端和 gRPC 原生客户端的服务。

## 为什么使用 sRPC？

Connect 因其简单和浏览器兼容性是新项目的理想选择，而 sRPC 则专注于兼容性。它为现有 gRPC 项目提供了一种快速支持 HTTP 和 Web 客户端的方式，无需对现有代码进行大幅修改。使用 sRPC，您可以：

- **保持 gRPC 兼容性**：继续使用现有的 gRPC 服务和接口。
- **扩展客户端支持**：让 HTTP 和浏览器客户端也能与服务交互。
- **减少迁移工作量**：避免重写或大幅修改现有 gRPC 代码。

## 功能

- **协议兼容性**：实现 Connect RPC 协议，支持浏览器和 gRPC 兼容的 HTTP API。
- **gRPC 接口兼容**：提供与 gRPC 相同的 API 体验，轻松过渡。
- **标准库兼容**：与 Go 标准库和 gRPC 代码兼容。
- **轻量设计**：专注核心功能，无多余复杂性。
- **完整流支持**：支持单向调用、服务器流、客户端流和双向流。
- **拦截器和中间件**：灵活的请求/响应处理管道。
- **错误处理**：结构化错误类型，兼容 Go 标准错误和 gRPC 状态码。
- **传输无关性**：支持 RPC、HTTP/1.1 和 HTTP/2。

## 快速开始

### 安装

```bash
go get github.com/opensraph/srpc
```

### 定义服务

使用标准的 Protocol Buffers 定义服务：

```protobuf
syntax = "proto3";

package srpc.examples.echo;

// EchoRequest 是回显请求。
message EchoRequest {
  string message = 1;
}

// EchoResponse 是回显响应。
message EchoResponse {
  string message = 1;
}

// Echo 是回显服务。
service Echo {
  // UnaryEcho 是单向回显。
  rpc UnaryEcho(EchoRequest) returns (EchoResponse) {}
  // ServerStreamingEcho 是服务器流。
  rpc ServerStreamingEcho(EchoRequest) returns (stream EchoResponse) {}
  // ClientStreamingEcho 是客户端流。
  rpc ClientStreamingEcho(stream EchoRequest) returns (EchoResponse) {}
  // BidirectionalStreamingEcho 是双向流。
  rpc BidirectionalStreamingEcho(stream EchoRequest) returns (stream EchoResponse) {}
}
```

### 服务端实现

sRPC 的服务端 API 完全兼容 gRPC，允许现有 gRPC 服务的无缝迁移。以下是一个带有认证和日志拦截器的服务端实现示例：

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

### 客户端使用

客户端 API 同样兼容 gRPC，同时支持标准库风格的调用模式。以下是一个带有日志和令牌注入拦截器的客户端实现示例：

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

### 使用 cURL 测试

您可以使用 `cURL` 测试 sRPC 服务器的 `UnaryEcho` 方法。以下是一个示例：

```bash
curl \
    --insecure \
    --header "Content-Type: application/json" \
    --data '{"message": "Hello sRPC"}' \
    https://localhost:50051/grpc.examples.echo.Echo/UnaryEcho
```

## 兼容性设计

sRPC 精心设计了与 Go 标准库和 gRPC 兼容的接口：

### 标准库兼容性

- 错误处理使用标准的 `error` 接口

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

### gRPC 兼容性

- 提供与 `grpc.ServiceRegistrar` 相同的注册接口
- 使用相同的服务描述符结构

## 示例

该仓库包含展示各种用例的工作示例：

- [基础服务端](examples/srpc-server/main.go) - 一个简单的 sRPC 服务端实现，带有认证和日志拦截器。服务端展示了单向和双向流 RPC、令牌验证以及基于 TLS 的凭据。
- [基础客户端](examples/srpc-client/main.go) - 一个对应的客户端实现，展示了单向和双向流 RPC 调用、日志和令牌注入拦截器以及基于 TLS 的安全连接。

## 贡献

欢迎贡献！请随时提交 Pull Request。

## 许可证

此项目根据 Apache License 2.0 许可 - 有关详细信息，请参阅 [LICENSE](LICENSE) 文件。
