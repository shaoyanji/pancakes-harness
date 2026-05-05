package backend_test

import (
	"context"
	"testing"
	"time"

	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/replay"
)

func TestBackendAdapterCanBeSwappedWithoutRuntimeChanges(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()

	runScenario := func(t *testing.T, name string, b backend.Backend) {
		t.Helper()
		ctx := context.Background()
		ts := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)

		events := []eventlog.Event{
			{ID: "e1", SessionID: "s1", TS: ts, Kind: eventlog.KindTurnUser, BranchID: "main"},
			{ID: "e2", SessionID: "s1", TS: ts.Add(time.Second), Kind: eventlog.KindToolRequest, BranchID: "main"},
			{ID: "e3", SessionID: "s1", TS: ts.Add(2 * time.Second), Kind: eventlog.KindToolResult, BranchID: "main", BlobRef: "blob://tool/1"},
		}
		for _, e := range events {
			if err := b.AppendEvent(ctx, e); err != nil {
				t.Fatalf("%s append event: %v", name, err)
			}
		}
		if err := b.AppendBlob(ctx, "blob://tool/1", []byte("payload")); err != nil {
			t.Fatalf("%s append blob: %v", name, err)
		}

		sessionEvents, err := b.ListEventsBySession(ctx, "s1")
		if err != nil {
			t.Fatalf("%s list session: %v", name, err)
		}
		state, err := replay.RebuildSession(sessionEvents)
		if err != nil {
			t.Fatalf("%s rebuild session: %v", name, err)
		}
		if state.BranchHeads["main"] != "e3" {
			t.Fatalf("%s expected head e3, got %q", name, state.BranchHeads["main"])
		}

		branchEvents, err := b.ListEventsByBranch(ctx, "s1", "main")
		if err != nil {
			t.Fatalf("%s list branch: %v", name, err)
		}
		if len(branchEvents) != 3 {
			t.Fatalf("%s expected 3 branch events, got %d", name, len(branchEvents))
		}

		blob, err := b.FetchBlob(ctx, "blob://tool/1")
		if err != nil {
			t.Fatalf("%s fetch blob: %v", name, err)
		}
		if string(blob) != "payload" {
			t.Fatalf("%s expected payload blob, got %q", name, string(blob))
		}
	}

	runScenario(t, "memory", mem)
}
