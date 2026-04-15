package memory

import (
	"fmt"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
)

func TestCompactByScore(t *testing.T) {
	events := make([]eventlog.Event, 10)
	scores := make(map[string]float64)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("ev.%03d", i)
		events[i] = eventlog.Event{
			ID: id, SessionID: "s", BranchID: "m", Kind: "turn.user",
			TS: time.Now().Add(time.Duration(i) * time.Minute),
		}
		// Lower scores for earlier events
		scores[id] = float64(i) * 0.1
	}

	cfg := CompactionConfig{KeepRecent: 3, MaxCompactEvents: 5}
	result, kept := CompactByScore(events, scores, cfg)

	if !result.Compacted {
		t.Fatal("expected compaction to occur")
	}
	if result.OriginalCount != 10 {
		t.Fatalf("expected original=10, got %d", result.OriginalCount)
	}
	if len(kept) != result.CompactedCount {
		t.Fatalf("kept count mismatch: %d vs %d", len(kept), result.CompactedCount)
	}
	// Last 3 events must be kept
	for i := 7; i < 10; i++ {
		found := false
		for _, e := range kept {
			if e.ID == events[i].ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("recent event %s not kept", events[i].ID)
		}
	}
}

func TestCompactByTextSize(t *testing.T) {
	events := make([]eventlog.Event, 5)
	for i := 0; i < 5; i++ {
		textLen := 100 + i*200 // increasing text sizes
		events[i] = eventlog.Event{
			ID: fmt.Sprintf("ev.%03d", i), SessionID: "s", BranchID: "m", Kind: "turn.user",
			TS: time.Now().Add(time.Duration(i) * time.Minute),
			Meta: map[string]any{"text": string(make([]byte, textLen))},
		}
	}

	cfg := CompactionConfig{KeepRecent: 2, MaxCompactEvents: 3}
	result, kept := CompactByTextSize(events, 500, cfg)

	if !result.Compacted {
		t.Fatal("expected compaction to occur")
	}
	if result.BytesSaved <= 0 {
		t.Fatal("expected bytes to be saved")
	}
	// Recent 2 must be kept
	lastID := events[4].ID
	found := false
	for _, e := range kept {
		if e.ID == lastID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("recent event was removed but should be kept")
	}
}

func TestShouldCompact(t *testing.T) {
	cfg := CompactionConfig{TriggerTurns: 10, BudgetPressureRatio: 0.8}

	// Trigger by turn count
	if !ShouldCompact(10, 0.5, cfg) {
		t.Fatal("should compact by turn count")
	}

	// Trigger by budget pressure
	if !ShouldCompact(3, 0.85, cfg) {
		t.Fatal("should compact by budget pressure")
	}

	// No trigger
	if ShouldCompact(5, 0.5, cfg) {
		t.Fatal("should not compact")
	}
}

func TestCompactByScoreNoCompactionNeeded(t *testing.T) {
	events := []eventlog.Event{
		{ID: "1", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now()},
		{ID: "2", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now()},
	}
	scores := map[string]float64{"1": 0.5, "2": 0.3}

	cfg := CompactionConfig{KeepRecent: 5, MaxCompactEvents: 10}
	result, kept := CompactByScore(events, scores, cfg)

	if result.Compacted {
		t.Fatal("should not compact when under KeepRecent")
	}
	if len(kept) != 2 {
		t.Fatalf("expected 2 kept, got %d", len(kept))
	}
}

func TestCompactByTextSizeUnderBudget(t *testing.T) {
	events := []eventlog.Event{
		{ID: "1", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now(), Meta: map[string]any{"text": "short"}},
		{ID: "2", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now(), Meta: map[string]any{"text": "short"}},
	}

	cfg := CompactionConfig{KeepRecent: 1, MaxCompactEvents: 10}
	result, kept := CompactByTextSize(events, 10000, cfg)

	if result.Compacted {
		t.Fatal("should not compact when under budget")
	}
	if len(kept) != 2 {
		t.Fatalf("expected 2 kept, got %d", len(kept))
	}
}
