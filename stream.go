package srpc

import (
	"fmt"

	"github.com/opensraph/srpc/protocol"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc"
)

type StreamType protocol.StreamType

const (
	// StreamTypeUnary indicates a non-streaming RPC.
	StreamTypeUnary StreamType = StreamType(protocol.StreamTypeUnary)
	// StreamTypeClient indicates client-side streaming.
	StreamTypeClient StreamType = StreamType(protocol.StreamTypeClient)
	// StreamTypeServer indicates server-side streaming.
	StreamTypeServer StreamType = StreamType(protocol.StreamTypeServer)
	// StreamTypeBidi indicates bidirectional streaming.
	StreamTypeBidi StreamType = StreamType(protocol.StreamTypeBidi)
)

func (s StreamType) String() string {
	switch s {
	case StreamTypeUnary:
		return "unary"
	case StreamTypeClient:
		return "client"
	case StreamTypeServer:
		return "server"
	case StreamTypeBidi:
		return "bidi"
	}
	return fmt.Sprintf("stream_%d", s)
}

func (s StreamType) IsClient() bool {
	return s == StreamTypeClient || s == StreamTypeBidi
}

func (s StreamType) IsServer() bool {
	return s == StreamTypeServer || s == StreamTypeBidi
}

// IdempotencyLevel defines the idempotency level of an RPC method.
// It affects whether the request can be safely retried and what request patterns are allowed.
type IdempotencyLevel protocol.IdempotencyLevel

// These values should match google.protobuf.MethodOptions.IdempotencyLevel.
const (
	// IdempotencyUnknown means the idempotency level is unspecified.
	// Methods with this level may or may not be idempotent.
	IdempotencyUnknown IdempotencyLevel = IdempotencyLevel(protocol.IdempotencyUnknown)

	// IdempotencyNoSideEffects means the method has no side effects.
	// It's semantically equivalent to "safe" methods in RFC 9110 Section 9.2.1.
	// Suitable for read-only operations like HTTP GET.
	// Requests can be safely retried.
	IdempotencyNoSideEffects IdempotencyLevel = IdempotencyLevel(protocol.IdempotencyNoSideEffects)

	// IdempotencyIdempotent means the method is idempotent.
	// Multiple identical requests have the same effect as a single request.
	// Equivalent to "idempotent" methods in RFC 9110 Section 9.2.2.
	// Suitable for operations like delete or update that can be safely retried.
	IdempotencyIdempotent IdempotencyLevel = IdempotencyLevel(protocol.IdempotencyIdempotent)
)

func (i IdempotencyLevel) String() string {
	switch i {
	case IdempotencyUnknown:
		return "idempotency_unknown"
	case IdempotencyNoSideEffects:
		return "no_side_effects"
	case IdempotencyIdempotent:
		return "idempotent"
	}
	return fmt.Sprintf("idempotency_%d", i)
}

// StreamDesc is a description of a client call or a handler invocation.
//
// If you're using Protobuf, protoc-gen-connect-go generates a constant for the
// fully-qualified Procedure corresponding to each RPC in your schema.
type StreamDesc struct {
	// ServiceName is the name of the service. (e.g., "service.v1.Service")
	ServiceName string
	// MethodName is the name of the method being called. (e.g., "Method")
	MethodName string
	// Contains the implementation for the methods in this service.
	ServiceImpl any
	// StreamType indicates the streaming direction.
	StreamType StreamType
	// Handler is the function that handles the RPC. (e.g., [grpc.MethodHandler] or [grpc.StreamHandler]).
	Handler any
	// IsClient indicates if this is a client-side specification.
	IsClient bool
	// IdempotencyLevel indicates the RPC's idempotency level.
	IdempotencyLevel IdempotencyLevel
}

func parseGrpcStreamType(desc *grpc.StreamDesc) StreamType {
	if desc.ClientStreams && desc.ServerStreams {
		return StreamTypeBidi
	} else if desc.ClientStreams {
		return StreamTypeClient
	} else if desc.ServerStreams {
		return StreamTypeServer
	}
	return StreamTypeUnary
}
