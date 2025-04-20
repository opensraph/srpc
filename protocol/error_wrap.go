package protocol

import (
	"context"
	"fmt"
	"net/http"

	"github.com/opensraph/srpc/errors"
)

// errorTranslatingHandlerConnCloser wraps a handlerConnCloser to ensure that
// we always return coded errors to users and write coded errors to the
// network.
//
// It's used in protocol implementations.
type errorTranslatingHandlerConnCloser struct {
	HandlerConnCloser

	toWire   func(error) error
	fromWire func(error) error
}

func (hc *errorTranslatingHandlerConnCloser) Send(msg any) error {
	return hc.fromWire(hc.HandlerConnCloser.Send(msg))
}

func (hc *errorTranslatingHandlerConnCloser) Receive(msg any) error {
	return hc.fromWire(hc.HandlerConnCloser.Receive(msg))
}

func (hc *errorTranslatingHandlerConnCloser) Close(err error) error {
	closeErr := hc.HandlerConnCloser.Close(hc.toWire(err))
	return hc.fromWire(closeErr)
}

func (hc *errorTranslatingHandlerConnCloser) getHTTPMethod() string {
	if method, ok := hc.HandlerConnCloser.(interface{ getHTTPMethod() string }); ok {
		return method.getHTTPMethod()
	}
	return http.MethodPost
}

// errorTranslatingClientConn wraps a StreamingClientConn to make sure that we always
// return coded errors from clients.
//
// It's used in protocol implementations.
type errorTranslatingClientConn struct {
	StreamingClientConn

	fromWire func(error) error
}

func (cc *errorTranslatingClientConn) Send(msg any) error {
	return cc.fromWire(cc.StreamingClientConn.Send(msg))
}

func (cc *errorTranslatingClientConn) Receive(msg any) error {
	return cc.fromWire(cc.StreamingClientConn.Receive(msg))
}

func (cc *errorTranslatingClientConn) CloseRequest() error {
	return cc.fromWire(cc.StreamingClientConn.CloseRequest())
}

func (cc *errorTranslatingClientConn) CloseResponse() error {
	return cc.fromWire(cc.StreamingClientConn.CloseResponse())
}

func (cc *errorTranslatingClientConn) OnRequestSend(fn func(*http.Request)) {
	cc.StreamingClientConn.OnRequestSend(fn)
}

// WrapHandlerConnWithCodedErrors ensures that we (1) automatically code
// context-related errors correctly when writing them to the network, and (2)
// return *Errors from all exported APIs.
func WrapHandlerConnWithCodedErrors(conn HandlerConnCloser) HandlerConnCloser {
	return &errorTranslatingHandlerConnCloser{
		HandlerConnCloser: conn,
		toWire:            errors.FromContextError,
		fromWire:          wrapIfUncoded,
	}
}

// WrapClientConnWithCodedErrors ensures that we always return *Errors from
// public APIs.
func WrapClientConnWithCodedErrors(conn StreamingClientConn) StreamingClientConn {
	return &errorTranslatingClientConn{
		StreamingClientConn: conn,
		fromWire:            wrapIfUncoded,
	}
}

// wrapIfUncoded ensures that all errors are wrapped. It leaves already-wrapped
// errors unchanged, uses wrapIfContextError to apply codes to context.Canceled
// and context.DeadlineExceeded, and falls back to wrapping other errors with
// CodeUnknown.
func wrapIfUncoded(err error) error {
	if err == nil {
		return nil
	}
	maybeCodedErr := errors.FromContextError(err)
	if ok := errors.As(maybeCodedErr, new(*errors.Error)); ok {
		return maybeCodedErr
	}
	return errors.FromError(err).WithCode(errors.Unknown)
}

// WrapIfContextDone wraps errors with CodeCanceled or CodeDeadlineExceeded
// if the context is done. It leaves already-wrapped errors unchanged.
func WrapIfContextDone(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	err = errors.FromContextError(err)
	if ok := errors.As(err, new(*errors.Error)); ok {
		return err
	}
	ctxErr := ctx.Err()
	if errors.Is(ctxErr, context.Canceled) {
		return errors.FromError(err).WithCode(errors.Canceled)
	} else if errors.Is(ctxErr, context.DeadlineExceeded) {
		return errors.FromError(err).WithCode(errors.DeadlineExceeded)
	}
	return err
}

// WrapIfMaxBytesError wraps errors returned reading from a http.MaxBytesHandler
// whose limit has been exceeded.
func WrapIfMaxBytesError(err error, tmpl string, args ...any) error {
	if err == nil {
		return nil
	}
	if ok := errors.As(err, new(*errors.Error)); ok {
		return err
	}
	var maxBytesErr *http.MaxBytesError
	if ok := errors.As(err, &maxBytesErr); !ok {
		return err
	}
	prefix := fmt.Sprintf(tmpl, args...)
	return errors.Newf("%s: exceeded %d byte http.MaxBytesReader limit", prefix, maxBytesErr.Limit).WithCode(errors.ResourceExhausted)
}
