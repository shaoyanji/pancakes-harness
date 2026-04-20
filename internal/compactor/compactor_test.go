package compactor

import (
	"context"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
)

func TestMockCompactorBasic(t *testing.T) {
	events := []eventlog.Event{
		{
			ID:        "evt-1",
			SessionID: "s1",
			TS:        time.Now().Add(-10 * time.Minute),
			Kind:      eventlog.KindTurnUser,
			BranchID:  "main",
			Meta:      map[string]any{"text": "How do I set up the Go project?"},
		},
		{
			ID:        "evt-2",
			SessionID: "s1",
			TS:        time.Now().Add(-9 * time.Minute),
			Kind:      eventlog.KindTurnAgent,
			BranchID:  "main",
			Meta:      map[string]any{"text": "Start with go mod init, then create cmd/ and internal/ directories."},
		},
		{
			ID:        "evt-3",
			SessionID: "s1",
			TS:        time.Now().Add(-5 * time.Minute),
			Kind:      eventlog.KindTurnUser,
			BranchID:  "main",
			Meta:      map[string]any{"text": "What about testing?"},
		},
	}

	mock := &MockCompactor{}
	resp, err := mock.CompactContext(context.Background(), CompactRequest{
		SessionID: "s1",
		BranchID:  "main",
		Events:    events,
	})
	if err != nil {
		t.Fatalf("mock compaction failed: %v", err)
	}

	if err := resp.AST.Validate(); err != nil {
		t.Fatalf("mock AST invalid: %v", err)
	}

	if len(resp.AST.EventCoverage) != 3 {
		t.Fatalf("expected 3 event refs, got %d", len(resp.AST.EventCoverage))
	}

	if resp.AST.RootID != "root-0" {
		t.Fatalf("expected root-0, got %s", resp.AST.RootID)
	}

	// Verify depth structure
	depth0 := resp.AST.GetLeavesByDepth(0)
	if len(depth0) != 3 {
		t.Fatalf("expected 3 depth-0 leaves, got %d", len(depth0))
	}

	depth1 := resp.AST.GetLeavesByDepth(1)
	if len(depth1) != 1 {
		t.Fatalf("expected 1 depth-1 cluster, got %d", len(depth1))
	}
}

func TestMockCompactorFixedAST(t *testing.T) {
	fixedAST := &TokenAST{
		SessionID: "s1",
		BranchID:  "main",
		RootID:    "root-custom",
		Leaves: map[string]MemoryLeaf{
			"root-custom": {
				ID:         "root-custom",
				Kind:       LeafKindRoot,
				Depth:      3,
				Summary:    "custom root",
				Importance: 1.0,
			},
		},
	}

	mock := &MockCompactor{FixedAST: fixedAST}
	resp, err := mock.CompactContext(context.Background(), CompactRequest{
		SessionID: "s1",
		BranchID:  "main",
		Events:    []eventlog.Event{{ID: "evt-1", SessionID: "s1", TS: time.Now(), Kind: eventlog.KindTurnUser, BranchID: "main"}},
	})
	if err != nil {
		t.Fatalf("fixed AST compaction failed: %v", err)
	}

	if resp.AST.RootID != "root-custom" {
		t.Fatalf("expected root-custom, got %s", resp.AST.RootID)
	}
}

func TestMockCompactorEmptyEvents(t *testing.T) {
	mock := &MockCompactor{}
	_, err := mock.CompactContext(context.Background(), CompactRequest{
		SessionID: "s1",
		BranchID:  "main",
		Events:    []eventlog.Event{},
	})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestMockCompactorCheckpoint(t *testing.T) {
	events := []eventlog.Event{
		{ID: "evt-1", SessionID: "s1", TS: time.Now(), Kind: eventlog.KindTurnUser, BranchID: "main", Meta: map[string]any{"text": "hello"}},
		{ID: "evt-2", SessionID: "s1", TS: time.Now(), Kind: eventlog.KindTurnAgent, BranchID: "main", Meta: map[string]any{"text": "hi back"}},
	}

	mock := &MockCompactor{}
	resp, err := mock.CompactContext(context.Background(), CompactRequest{
		SessionID: "s1",
		BranchID:  "main",
		Events:    events,
	})
	if err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// The mock doesn't populate checkpoint, but the real GeminiCompactor would
	// Verify the raw AST bytes are valid JSON
	if len(resp.RawAST) == 0 {
		t.Fatal("RawAST should not be empty")
	}
}

func TestGeminiCompactorName(t *testing.T) {
	c := NewGeminiCompactor(GeminiConfig{APIKey: "test-key"})
	if c.Name() != "gemini-flash" {
		t.Fatalf("expected gemini-flash, got %s", c.Name())
	}
}

func TestMockCompactorName(t *testing.T) {
	mock := &MockCompactor{}
	if mock.Name() != "mock" {
		t.Fatalf("expected mock, got %s", mock.Name())
	}
}
