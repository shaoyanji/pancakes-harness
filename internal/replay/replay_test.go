package replay

import (
	"context"
	"reflect"
	"testing"
	"time"

	"pancakes-harness/internal/branchdag"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/summaries"
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

func TestSummaryCheckpointRebuildWorks(t *testing.T) {
	t.Parallel()

	baseBranch := branchdag.Branch{
		BranchID:      "main",
		BaseSummaryID: "sum-1",
	}
	checkpoint := summaries.SummaryCheckpoint{
		SummaryID:    "sum-1",
		BranchID:     "main",
		BasisEventID: "e2",
		CoveredRange: summaries.CoveredRange{
			StartEventID: "e1",
			EndEventID:   "e2",
		},
		BlobRef: "blob://sum-1",
	}
	ts := time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)
	delta := []eventlog.Event{
		{
			ID:        "e3",
			SessionID: "s1",
			TS:        ts,
			Kind:      eventlog.KindTurnUser,
			BranchID:  "main",
		},
		{
			ID:        "e4",
			SessionID: "s1",
			TS:        ts.Add(time.Second),
			Kind:      eventlog.KindTurnAgent,
			BranchID:  "main",
		},
	}

	got, err := RebuildBranchFromSummaryDelta(baseBranch, checkpoint, delta)
	if err != nil {
		t.Fatalf("rebuild summary+delta: %v", err)
	}
	if got.HeadEventID != "e4" {
		t.Fatalf("expected head e4, got %q", got.HeadEventID)
	}
	if got.BaseSummaryID != "sum-1" {
		t.Fatalf("expected base summary sum-1, got %q", got.BaseSummaryID)
	}
	if len(got.DirtyRanges) != 1 || got.DirtyRanges[0].StartEventID != "e3" || got.DirtyRanges[0].EndEventID != "e4" {
		t.Fatalf("unexpected dirty ranges: %#v", got.DirtyRanges)
	}
}

func TestReplayReconstructsBranchStateFromSummaryAndDelta(t *testing.T) {
	t.Parallel()

	branch := branchdag.Branch{BranchID: "main"}
	checkpoint := summaries.SummaryCheckpoint{
		SummaryID:    "sum-1",
		BranchID:     "main",
		BasisEventID: "e2",
		CoveredRange: summaries.CoveredRange{
			StartEventID: "e1",
			EndEventID:   "e2",
		},
		TextRef: "summary://s1",
	}

	ts := time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)
	sessionEvents := []eventlog.Event{
		{ID: "e1", SessionID: "s1", TS: ts, Kind: eventlog.KindTurnUser, BranchID: "main"},
		{ID: "e2", SessionID: "s1", TS: ts.Add(time.Second), Kind: eventlog.KindTurnAgent, BranchID: "main"},
		{ID: "e3", SessionID: "s1", TS: ts.Add(2 * time.Second), Kind: eventlog.KindTurnUser, BranchID: "main"},
		{ID: "x1", SessionID: "s1", TS: ts.Add(3 * time.Second), Kind: eventlog.KindTurnUser, BranchID: "other"},
		{ID: "e4", SessionID: "s1", TS: ts.Add(4 * time.Second), Kind: eventlog.KindTurnAgent, BranchID: "main"},
	}

	got, err := RebuildBranchFromSummaryAndEvents(branch, checkpoint, sessionEvents)
	if err != nil {
		t.Fatalf("rebuild from summary+events: %v", err)
	}
	if got.HeadEventID != "e4" {
		t.Fatalf("expected head e4, got %q", got.HeadEventID)
	}
	if len(got.DirtyRanges) != 1 || got.DirtyRanges[0].StartEventID != "e3" || got.DirtyRanges[0].EndEventID != "e4" {
		t.Fatalf("unexpected dirty ranges: %#v", got.DirtyRanges)
	}
}

func TestParentChildBranchLineagePreserved(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)
	events := []eventlog.Event{
		{ID: "e1", SessionID: "s1", TS: ts, Kind: eventlog.KindTurnUser, BranchID: "main"},
		{ID: "e2", SessionID: "s1", TS: ts.Add(time.Second), Kind: eventlog.KindTurnAgent, BranchID: "main"},
		{
			ID:            "e3",
			SessionID:     "s1",
			TS:            ts.Add(2 * time.Second),
			Kind:          eventlog.KindBranchFork,
			BranchID:      "alt",
			ParentEventID: "e2",
			Meta: map[string]any{
				"parent_branch_id": "main",
			},
		},
		{ID: "e4", SessionID: "s1", TS: ts.Add(3 * time.Second), Kind: eventlog.KindTurnUser, BranchID: "alt"},
	}

	branches, err := RebuildBranchStateFromEvents(events)
	if err != nil {
		t.Fatalf("rebuild branch state: %v", err)
	}
	alt := branches["alt"]
	if alt.ParentBranchID != "main" {
		t.Fatalf("expected alt parent main, got %q", alt.ParentBranchID)
	}
	if alt.ForkEventID != "e2" {
		t.Fatalf("expected alt fork event e2, got %q", alt.ForkEventID)
	}
	if alt.HeadEventID != "e4" {
		t.Fatalf("expected alt head e4, got %q", alt.HeadEventID)
	}
}

func TestReplayPreservesToolEventsCleanly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := eventlog.NewMemoryStore()
	ts := time.Date(2026, 3, 2, 3, 4, 5, 0, time.UTC)

	events := []eventlog.Event{
		{
			ID:        "e1",
			SessionID: "s-tools",
			TS:        ts,
			Kind:      eventlog.KindTurnUser,
			BranchID:  "main",
		},
		{
			ID:        "e2",
			SessionID: "s-tools",
			TS:        ts.Add(time.Second),
			Kind:      eventlog.KindToolRequest,
			BranchID:  "main",
			Meta: map[string]any{
				"tool":    "echo_tool",
				"call_id": "c1",
			},
		},
		{
			ID:        "e3",
			SessionID: "s-tools",
			TS:        ts.Add(2 * time.Second),
			Kind:      eventlog.KindToolResult,
			BranchID:  "main",
			BlobRef:   "blob://tool-output",
			Meta: map[string]any{
				"tool":    "echo_tool",
				"call_id": "c1",
				"summary": "ok",
			},
		},
	}

	for _, e := range events {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}

	sessionState, err := RebuildFromStore(ctx, store, "s-tools")
	if err != nil {
		t.Fatalf("session replay: %v", err)
	}
	if sessionState.EventCount != 3 {
		t.Fatalf("expected 3 events, got %d", sessionState.EventCount)
	}
	if sessionState.BranchHeads["main"] != "e3" {
		t.Fatalf("expected main head e3, got %q", sessionState.BranchHeads["main"])
	}

	stored, err := store.ListBySession(ctx, "s-tools")
	if err != nil {
		t.Fatalf("list session: %v", err)
	}
	branches, err := RebuildBranchStateFromEvents(stored)
	if err != nil {
		t.Fatalf("branch replay: %v", err)
	}
	main := branches["main"]
	if main.HeadEventID != "e3" {
		t.Fatalf("expected branch head e3, got %q", main.HeadEventID)
	}
	if len(main.DirtyRanges) != 1 || main.DirtyRanges[0].StartEventID != "e1" || main.DirtyRanges[0].EndEventID != "e3" {
		t.Fatalf("unexpected dirty range after tool events: %#v", main.DirtyRanges)
	}
}
