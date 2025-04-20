package errors

import (
	"context"
	"fmt"
	"net/http"
)

// WrapIfMaxBytesError wraps errors returned reading from a http.MaxBytesHandler
// whose limit has been exceeded.
func WrapIfMaxBytesError(err error, tmpl string, args ...any) error {
	if err == nil {
		return nil
	}
	if ok := As(err, new(*Error)); ok {
		return err
	}
	var maxBytesErr *http.MaxBytesError
	if ok := As(err, &maxBytesErr); !ok {
		return err
	}
	prefix := fmt.Sprintf(tmpl, args...)
	return Newf("%s: exceeded %d byte http.MaxBytesReader limit", prefix, maxBytesErr.Limit).WithCode(ResourceExhausted)
}

// WrapIfContextDone wraps errors with CodeCanceled or CodeDeadlineExceeded
// if the context is done. It leaves already-wrapped errors unchanged.
func WrapIfContextDone(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	err = FromContextError(err)
	if ok := As(err, new(*Error)); ok {
		return err
	}
	ctxErr := ctx.Err()
	if Is(ctxErr, context.Canceled) {
		return FromError(err).WithCode(Canceled)
	} else if Is(ctxErr, context.DeadlineExceeded) {
		return FromError(err).WithCode(DeadlineExceeded)
	}
	return err
}
