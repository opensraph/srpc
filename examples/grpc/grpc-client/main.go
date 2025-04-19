package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/opensraph/srpc"
	elizav1 "github.com/opensraph/srpc/examples/proto/gen/srpc/eliza/v1"
)

var (
	serverAddr = flag.String("addr", "localhost:8080", "The server address in the format of host:port")
)

func init() {
	flag.Parse()
}

func main() {
	conn := srpc.NewClient(*serverAddr)

	client := elizav1.NewElizaServiceClient(conn)

	fmt.Print("What is your name? ")
	input := bufio.NewReader(os.Stdin)
	str, err := input.ReadString('\n')
	if err != nil {
		log.Fatalf("error reading input: %v", err)
	}

	res, err := client.Say(context.Background(), &elizav1.SayRequest{
		Sentence: str,
	})
	if err != nil {
		log.Fatalf("error sending request: %v", err)
	}
	fmt.Println("eliza: ", res.GetSentence())

	stream, err := client.Introduce(
		context.Background(),
		&elizav1.IntroduceRequest{
			Name: str,
		},
	)
	if err != nil {
		log.Fatalf("error creating stream: %v", err)
	}
	for {
		res, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			log.Fatalf("error receiving message: %v", err)
		}
		if res == nil {
			log.Fatalf("error receiving message: %v", err)
		}

		fmt.Println("eliza: ", res.GetSentence())
	}

	fmt.Println()

	for {
		fmt.Print("you: ")
		input := bufio.NewReader(os.Stdin)
		str, err := input.ReadString('\n')
		if err != nil {
			log.Fatalf("error reading input: %v", err)
		}

		resp, err := client.Say(context.Background(), &elizav1.SayRequest{Sentence: str})
		if err != nil {
			log.Fatalf("error sending request: %v", err)
		}
		fmt.Println("eliza: ", resp.GetSentence())
		fmt.Println()
	}
}
