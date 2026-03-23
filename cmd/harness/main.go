package main

import (
	"context"
	"fmt"
	"time"

	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/replay"
)

func main() {
	store := eventlog.NewMemoryStore()
	_ = store.Append(context.Background(), eventlog.Event{
		ID:        "bootstrap",
		SessionID: "demo",
		TS:        time.Now().UTC(),
		Kind:      "system.warning",
		BranchID:  "main",
	})

	state, err := replay.RebuildFromStore(context.Background(), store, "demo")
	if err != nil {
		panic(err)
	}
	fmt.Printf("session=%s events=%d head=%s\n", state.SessionID, state.EventCount, state.LastEventID)
}
