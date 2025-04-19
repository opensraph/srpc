package protocol

import "fmt"

// IdempotencyLevel defines the idempotency level of an RPC method.
// It affects whether the request can be safely retried and what request patterns are allowed.
type IdempotencyLevel int

// These values should match google.protobuf.MethodOptions.IdempotencyLevel.
const (
	// IdempotencyUnknown means the idempotency level is unspecified.
	// Methods with this level may or may not be idempotent.
	IdempotencyUnknown IdempotencyLevel = 0

	// IdempotencyNoSideEffects means the method has no side effects.
	// It's semantically equivalent to "safe" methods in RFC 9110 Section 9.2.1.
	// Suitable for read-only operations like HTTP GET.
	// Requests can be safely retried.
	IdempotencyNoSideEffects IdempotencyLevel = 1

	// IdempotencyIdempotent means the method is idempotent.
	// Multiple identical requests have the same effect as a single request.
	// Equivalent to "idempotent" methods in RFC 9110 Section 9.2.2.
	// Suitable for operations like delete or update that can be safely retried.
	IdempotencyIdempotent IdempotencyLevel = 2
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
