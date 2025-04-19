package errors

import (
	"context"
	"fmt"
	"net/http"
	"os"

	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const errorFormat = "code = %d desc = %s"

var _ (interface{ Unwrap() error }) = (*Error)(nil)
var _ error = (*Error)(nil)

type Error struct {
	code    Code
	err     error
	status  *status.Status
	wireErr bool
	meta    http.Header
}

func New(text string) *Error {
	return &Error{
		code:   Unknown,
		status: status.New(codes.Unknown, text),
		meta:   make(http.Header),
	}
}

func Newf(format string, a ...any) *Error {
	err := fmt.Errorf(format, a...)
	return FromError(err)
}

func FromError(err error) *Error {
	s, _ := status.FromError(err)
	return &Error{
		code:   Unknown,
		err:    err,
		status: s,
	}
}

func AsError(err error) (*Error, bool) {
	var srpcErr *Error
	ok := As(err, &srpcErr)
	return srpcErr, ok
}

func FromProto(s *spb.Status) *Error {
	return &Error{
		status: status.FromProto(s),
		err:    status.FromProto(s).Err(),
	}
}

func (e *Error) Error() string {
	return e.status.Message()
}

func (e *Error) Equals(err error) bool {
	if e == nil || err == nil {
		return e == err
	}
	return e.Equals(err)
}

// Unwrap allows [ge.Is] and [ge.As] access to the underlying error.
func (e *Error) Unwrap() error {
	if e.err != nil {
		return e.err
	}
	return e.status.Err()
}

func (e *Error) Code() Code {
	if e.code != 0 {
		return e.code
	}
	return Code(e.status.Code())
}

func (e *Error) Details() []any {
	return e.status.Details()
}

// Proto returns s's status as an spb.Status proto message.
func (e *Error) Proto() *spb.Status {
	return e.Proto()
}

func (e *Error) WithCode(code Code) *Error {
	e.code = code
	return e
}

// WithDetails returns a new status with the provided details messages appended to the status.
// If any errors are encountered, it returns nil and the first error encountered.
func (e *Error) WithDetails(details ...protoadapt.MessageV1) (*Error, error) {
	s, err := e.status.WithDetails(details...)
	e.status = s
	return e, err
}

func (e *Error) WithDetailFromMap(v map[string]any) (*Error, error) {
	s, err := structpb.NewStruct(v)
	if err != nil {
		return nil, err
	}
	return e.WithDetails(s)
}

func (e *Error) DetailsAsAny() []*anypb.Any {
	a := make([]*anypb.Any, 0, len(e.status.Details()))
	for _, detail := range e.status.Details() {
		a = append(a, detail.(*anypb.Any))
	}
	return a
}

// FromContextError converts a context error or wrapped context error into a
// Status.  It returns a Status with codes.OK if err is nil, or a Status with
// codes.Unknown if err is non-nil and not a context error.
func FromContextError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if ok := As(err, &e); ok {
		return e
	}
	if Is(err, context.Canceled) {
		return FromError(err).WithCode(Canceled)
	}
	if Is(err, context.DeadlineExceeded) {
		return FromError(err).WithCode(DeadlineExceeded)
	}
	// Ick, some dial errors can be returned as os.ErrDeadlineExceeded
	// instead of context.DeadlineExceeded :(
	// https://github.com/golang/go/issues/64449
	if Is(err, os.ErrDeadlineExceeded) {
		return FromError(err).WithCode(DeadlineExceeded)
	}
	return FromError(err).WithCode(Unknown)
}
