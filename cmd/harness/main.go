package main

import (
	"context"
	"fmt"

	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/runtime"
	"pancakes-harness/internal/tools"
)

func main() {
	s, err := runtime.StartSession(runtime.Config{
		SessionID: "demo",
		Backend:   backend.NewMemoryBackend(),
		ModelAdapter: model.MockAdapter{NameValue: "demo-mock", CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"demo response"}`), nil
		}},
		ToolRunner: tools.NewRunner(nil),
	})
	if err != nil {
		panic(err)
	}

	out, err := s.HandleUserTurn(context.Background(), "main", "hello harness")
	if err != nil {
		panic(err)
	}
	fmt.Printf("session=%s branch=%s answer=%q envelope_bytes=%d\n", out.SessionID, out.BranchID, out.Answer, out.PacketEnvelopeBytes)
}
