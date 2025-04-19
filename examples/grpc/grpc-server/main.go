package main

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/opensraph/srpc"
	elizav1 "github.com/opensraph/srpc/examples/proto/gen/srpc/eliza/v1"
)

func main() {
	srv := srpc.NewServer()

	elizav1.RegisterElizaServiceServer(srv, &elizaImpl{})

	lis, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}
	if err := srv.Serve(lis); err != nil {
		panic(err)
	}
}

type elizaImpl struct {
	elizav1.UnimplementedElizaServiceServer
}

func (e elizaImpl) Say(_ context.Context, _ *elizav1.SayRequest) (*elizav1.SayResponse, error) {
	// Our example therapist isn't very sophisticated.
	return &elizav1.SayResponse{Sentence: "Tell me more about that."}, nil
}

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
