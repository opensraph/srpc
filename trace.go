package srpc

import (
	"context"

	"golang.org/x/net/trace"
	"google.golang.org/grpc"
)

func traceUnaryInterceptor() UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *UnaryServerInfo, handler UnaryHandler) (resp any, err error) {
		tr := trace.New("srpc.unary", info.FullMethod)
		defer tr.Finish()

		tr.LazyPrintf("Request: %v", req)

		ctx = trace.NewContext(ctx, tr)
		// Call the handler
		resp, err = handler(ctx, req)
		if err != nil {
			tr.LazyPrintf("%s", err)
			tr.SetError()
		}
		tr.LazyPrintf("Response: %v", resp)

		return resp, err
	}
}

func traceStreamInterceptor() StreamServerInterceptor {
	return func(srv any, ss ServerStream, info *StreamServerInfo, handler StreamHandler) error {
		tr := trace.New("srpc.stream", info.FullMethod)
		defer tr.Finish()
		// Call the handler
		err := handler(srv, &traceServerStream{ss, tr})
		if err != nil {
			tr.LazyPrintf("%s", err)
			tr.SetError()
		}
		return err
	}
}

var _ grpc.ServerStream = (*traceServerStream)(nil)

type traceServerStream struct {
	grpc.ServerStream
	tr trace.Trace
}

func (t *traceServerStream) Context() context.Context {
	return trace.NewContext(t.ServerStream.Context(), t.tr)
}

func (t *traceServerStream) RecvMsg(m any) error {
	t.tr.LazyPrintf("RecvMsg: %T", m)
	err := t.ServerStream.RecvMsg(m)
	if err != nil {
		t.tr.LazyPrintf("%s", err)
		t.tr.SetError()
	}
	return err
}

func (t *traceServerStream) SendMsg(m any) error {
	t.tr.LazyPrintf("SendMsg: %T", m)
	err := t.ServerStream.SendMsg(m)
	if err != nil {
		t.tr.LazyPrintf("%s", err)
		t.tr.SetError()
	}
	return err
}
