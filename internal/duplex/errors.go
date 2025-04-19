package duplex

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/opensraph/srpc/errors"
)

const commonErrorsURL = "https://connectrpc.com/docs/go/common-errors"

// wrapIfLikelyH2CNotConfiguredError adds a wrapping error that has a message
// telling the caller that they likely need to use h2c but are using a raw http.Client{}.
//
// This happens when running a gRPC-only server.
// This is fragile and may break over time, and this should be considered a best-effort.
func wrapIfLikelyH2CNotConfiguredError(request *http.Request, err error) error {
	if err == nil {
		return nil
	}
	if url := request.URL; url != nil && url.Scheme != "http" {
		// If the scheme is not http, we definitely do not have an h2c error, so just return.
		return err
	}
	// net/http code has been investigated and there is no typing of any of these errors
	// they are all created with fmt.Errorf
	// grpc-go returns the first error 2/3-3/4 of the time, and the second error 1/4-1/3 of the time
	if errString := err.Error(); strings.HasPrefix(errString, `Post "`) &&
		(strings.Contains(errString, `net/http: HTTP/1.x transport connection broken: malformed HTTP response`) ||
			strings.HasSuffix(errString, `write: broken pipe`)) {
		return fmt.Errorf("possible h2c configuration issue when talking to gRPC server, see %s: %w", commonErrorsURL, err)
	}
	return err
}

// WrapIfLikelyWithGRPCNotUsedError adds a wrapping error that has a message
// telling the caller that they likely forgot to use connect.WithGRPC().
//
// This happens when running a gRPC-only server.
// This is fragile and may break over time, and this should be considered a best-effort.
func wrapIfLikelyWithGRPCNotUsedError(err error) error {
	if err == nil {
		return nil
	}
	// golang.org/x/net code has been investigated and there is no typing of this error
	// it is created with fmt.Errorf
	// http2/transport.go:573:	return nil, fmt.Errorf("http2: Transport: cannot retry err [%v] after Request.Body was written; define Request.GetBody to avoid this error", err)
	if errString := err.Error(); strings.HasPrefix(errString, `Post "`) &&
		strings.Contains(errString, `http2: Transport: cannot retry err`) &&
		strings.HasSuffix(errString, `after Request.Body was written; define Request.GetBody to avoid this error`) {
		return fmt.Errorf("possible missing connect.WithGPRC() client option when talking to gRPC server, see %s: %w", commonErrorsURL, err)
	}
	return err
}

// HTTP/2 has its own set of error codes, which it sends in RST_STREAM frames.
// When the server sends one of these errors, we should map it back into our
// RPC error codes following
// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#http2-transport-mapping.
//
// This would be vastly simpler if we were using x/net/http2 directly, since
// the StreamError type is exported. When x/net/http2 gets vendored into
// net/http, though, all these types become unexported...so we're left with
// string munging.
func wrapIfRSTError(err error) error {
	const (
		streamErrPrefix = "stream error: "
		fromPeerSuffix  = "; received from peer"
	)
	if err == nil {
		return nil
	}
	if urlErr := new(url.Error); errors.As(err, &urlErr) {
		// If we get an RST_STREAM error from http.Client.Do, it's wrapped in a
		// *url.Error.
		err = urlErr.Unwrap()
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, streamErrPrefix) {
		return err
	}
	if !strings.HasSuffix(msg, fromPeerSuffix) {
		return err
	}
	msg = strings.TrimSuffix(msg, fromPeerSuffix)
	i := strings.LastIndex(msg, ";")
	if i < 0 || i >= len(msg)-1 {
		return err
	}
	msg = msg[i+1:]
	msg = strings.TrimSpace(msg)
	switch msg {
	case "NO_ERROR", "PROTOCOL_ERROR", "INTERNAL_ERROR", "FLOW_CONTROL_ERROR",
		"SETTINGS_TIMEOUT", "FRAME_SIZE_ERROR", "COMPRESSION_ERROR", "CONNECT_ERROR":
		return errors.FromError(err).WithCode(errors.Internal)
	case "REFUSED_STREAM":
		return errors.FromError(err).WithCode(errors.Unavailable)
	case "CANCEL":
		return errors.FromError(err).WithCode(errors.Canceled)
	case "ENHANCE_YOUR_CALM":
		return errors.FromError(err).WithCode(errors.ResourceExhausted)
	case "INADEQUATE_SECURITY":
		return errors.Newf("transport security error: %s", msg).WithCode(errors.PermissionDenied)
	default:
		return err
	}
}
