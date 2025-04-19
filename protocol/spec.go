package protocol

// Spec is a description of a client call or a handler invocation.
//
// If you're using Protobuf, protoc-gen-connect-go generates a constant for the
// fully-qualified Procedure corresponding to each RPC in your schema.
type Spec struct {
	StreamType       StreamType
	Schema           any    // for protobuf RPCs, a protoreflect.MethodDescriptor
	Procedure        string // for example, "/acme.foo.v1.FooService/Bar"
	IsClient         bool   // otherwise we're in a handler
	IdempotencyLevel IdempotencyLevel
}
