package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"connectrpc.com/connect"
	elizav1 "github.com/opensraph/srpc/examples/proto/gen/srpc/eliza/v1"
	"github.com/opensraph/srpc/examples/proto/gen/srpc/eliza/v1/elizav1connect"
)

var (
	serverAddr = flag.String("addr", "http://localhost:8080", "The server address in the format of host:port")
)

func init() {
	flag.Parse()
}

func main() {
	client := elizav1connect.NewElizaServiceClient(
		http.DefaultClient, *serverAddr,
		// connect.WithHTTPGet(),
		connect.WithProtoJSON(),
	)

	fmt.Print("What is your name? ")
	input := bufio.NewReader(os.Stdin)
	str, err := input.ReadString('\n')
	if err != nil {
		log.Fatalf("error reading input: %v", err)
	}

	res, err := client.Say(context.Background(), connect.NewRequest(&elizav1.SayRequest{
		Sentence: str,
	}))
	if err != nil {
		log.Fatalf("error sending request: %v", err)
	}
	fmt.Println("eliza: ", res.Msg.GetSentence())

	stream, err := client.Introduce(
		context.Background(),
		connect.NewRequest(&elizav1.IntroduceRequest{
			Name: str,
		}),
	)
	if err != nil {
		log.Fatalf("error creating stream: %v", err)
	}
	for stream.Receive() {
		fmt.Println("eliza: ", stream.Msg().GetSentence())
	}
	if err := stream.Err(); err != nil {
		log.Fatalf("error receiving message: %v", err)
	}

	fmt.Println()

	for {
		fmt.Print("you: ")
		input := bufio.NewReader(os.Stdin)
		str, err := input.ReadString('\n')
		if err != nil {
			log.Fatalf("error reading input: %v", err)
		}

		resp, err := client.Say(context.Background(), connect.NewRequest(&elizav1.SayRequest{Sentence: str}))
		if err != nil {
			log.Fatalf("error sending request: %v", err)
		}
		fmt.Println("eliza: ", resp.Msg.GetSentence())
		fmt.Println()
	}
}
