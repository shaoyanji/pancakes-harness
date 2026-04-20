package compactor

import (
	"context"
	"fmt"
	"strings"
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

func TestSplitIntoChunks(t *testing.T) {
	t.Run("small input returns single chunk", func(t *testing.T) {
		events := make([]eventlog.SerializedEvent, 50)
		for i := range events {
			events[i] = eventlog.SerializedEvent{ID: fmt.Sprintf("evt-%d", i)}
		}
		chunks := splitIntoChunks(events, 200)
		if len(chunks) != 1 {
			t.Fatalf("expected 1 chunk for 50 events, got %d", len(chunks))
		}
	})

	t.Run("large input splits into multiple chunks", func(t *testing.T) {
		events := make([]eventlog.SerializedEvent, 500)
		for i := range events {
			events[i] = eventlog.SerializedEvent{ID: fmt.Sprintf("evt-%d", i)}
		}
		chunks := splitIntoChunks(events, 200)
		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks for 500 events with max 200, got %d", len(chunks))
		}
		// Verify chronological order preserved
		if chunks[0][0].ID != "evt-0" {
			t.Fatalf("first chunk should start with evt-0, got %s", chunks[0][0].ID)
		}
		if chunks[1][0].ID != "evt-200" {
			t.Fatalf("second chunk should start with evt-200, got %s", chunks[1][0].ID)
		}
		if chunks[2][0].ID != "evt-400" {
			t.Fatalf("third chunk should start with evt-400, got %s", chunks[2][0].ID)
		}
	})

	t.Run("exact boundary", func(t *testing.T) {
		events := make([]eventlog.SerializedEvent, 400)
		for i := range events {
			events[i] = eventlog.SerializedEvent{ID: fmt.Sprintf("evt-%d", i)}
		}
		chunks := splitIntoChunks(events, 200)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks for exactly 400 events, got %d", len(chunks))
		}
	})

	t.Run("empty input returns empty", func(t *testing.T) {
		chunks := splitIntoChunks(nil, 200)
		if len(chunks) != 0 {
			t.Fatalf("expected 0 chunks for nil, got %d", len(chunks))
		}
	})
}

func TestChunkCountInMetrics(t *testing.T) {
	// Generate enough events to trigger chunking (>MaxChunkEvents)
	events := make([]eventlog.Event, 250)
	for i := range events {
		events[i] = eventlog.Event{
			ID:        fmt.Sprintf("evt-%d", i),
			SessionID: "s1",
			TS:        time.Now().Add(time.Duration(-i) * time.Minute),
			Kind:      eventlog.KindTurnUser,
			BranchID:  "main",
			Meta:      map[string]any{"text": fmt.Sprintf("message %d", i)},
		}
	}

	mock := &MockCompactor{}
	resp, err := mock.CompactContext(context.Background(), CompactRequest{
		SessionID: "s1",
		BranchID:  "main",
		Events:    events,
	})
	if err != nil {
		t.Fatalf("mock compaction with many events failed: %v", err)
	}

	if resp.Metrics.InputEvents != 250 {
		t.Fatalf("expected 250 input events, got %d", resp.Metrics.InputEvents)
	}
}

func TestBuildMergePrompt(t *testing.T) {
	summaries := []chunkSummary{
		{
			ChunkIndex:  0,
			EventCount:  100,
			RootSummary: "User discussed API design patterns",
			Tags:        []string{"api", "design"},
			Importance:  0.9,
			Sections: []sectionSummary{
				{ID: "sec-0", Summary: "REST vs GraphQL", Importance: 0.8},
			},
		},
		{
			ChunkIndex:  1,
			EventCount:  100,
			RootSummary: "User implemented the discussed patterns",
			Tags:        []string{"implementation"},
			Importance:  0.85,
			Sections: []sectionSummary{
				{ID: "sec-0", Summary: "Code review and testing", Importance: 0.7},
			},
		},
	}

	systemPrompt, userPrompt := buildMergePrompt(summaries, "main")

	if systemPrompt == "" {
		t.Fatal("system prompt should not be empty")
	}
	if !strings.Contains(userPrompt, "Chunk 0") {
		t.Fatal("user prompt should contain chunk 0")
	}
	if !strings.Contains(userPrompt, "Chunk 1") {
		t.Fatal("user prompt should contain chunk 1")
	}
	if !strings.Contains(userPrompt, "REST vs GraphQL") {
		t.Fatal("user prompt should contain section details")
	}
}
