package replay

import (
	"context"
	"reflect"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
)

func TestRebuildFromStoreReconstructsBranchHeads(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := eventlog.NewMemoryStore()
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	events := []eventlog.Event{
		{
			ID:        "e1",
			SessionID: "s1",
			TS:        ts,
			Kind:      "turn.user",
			BranchID:  "main",
		},
		{
			ID:            "e2",
			SessionID:     "s1",
			TS:            ts.Add(time.Second),
			Kind:          "turn.agent",
			BranchID:      "main",
			ParentEventID: "e1",
		},
		{
			ID:            "e3",
			SessionID:     "s1",
			TS:            ts.Add(2 * time.Second),
			Kind:          "branch.fork",
			BranchID:      "alt",
			ParentEventID: "e1",
		},
	}

	for _, e := range events {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}

	state, err := RebuildFromStore(ctx, store, "s1")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if state.SessionID != "s1" {
		t.Fatalf("session mismatch: %q", state.SessionID)
	}
	if state.EventCount != 3 {
		t.Fatalf("expected 3 events, got %d", state.EventCount)
	}
	if state.LastEventID != "e3" {
		t.Fatalf("expected last event e3, got %q", state.LastEventID)
	}
	if got := state.BranchHeads["main"]; got != "e2" {
		t.Fatalf("main branch head mismatch: %q", got)
	}
	if got := state.BranchHeads["alt"]; got != "e3" {
		t.Fatalf("alt branch head mismatch: %q", got)
	}
}

func TestRebuildSessionDeterministic(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	events := []eventlog.Event{
		{
			ID:        "e1",
			SessionID: "s1",
			TS:        ts,
			Kind:      "turn.user",
			BranchID:  "main",
		},
		{
			ID:            "e2",
			SessionID:     "s1",
			TS:            ts.Add(time.Second),
			Kind:          "turn.agent",
			BranchID:      "main",
			ParentEventID: "e1",
		},
	}

	first, err := RebuildSession(events)
	if err != nil {
		t.Fatalf("first rebuild failed: %v", err)
	}
	second, err := RebuildSession(events)
	if err != nil {
		t.Fatalf("second rebuild failed: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay must be deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
}
