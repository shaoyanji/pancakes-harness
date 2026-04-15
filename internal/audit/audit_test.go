package audit

import (
	"context"
	"testing"

	"pancakes-harness/internal/eventlog"
)

func TestRecordTurn(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	result := tracker.RecordTurn(context.Background(), 100, 0, appendFn)
	if result.TokensUsed != 100 {
		t.Fatalf("expected 100 tokens, got %d", result.TokensUsed)
	}
	if result.TurnNumber != 1 {
		t.Fatalf("expected turn 1, got %d", result.TurnNumber)
	}
	if len(appended) != 1 {
		t.Fatalf("expected 1 event appended, got %d", len(appended))
	}
}

func TestShouldTerminate(t *testing.T) {
	cfg := Config{
		MaxTokensPerConsult:        1000,
		AutoTerminateOnAuditComplete: true,
	}
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	// First turn: under budget, should continue
	tracker.RecordTurn(context.Background(), 100, 0, appendFn)
	if tracker.ShouldTerminate() {
		t.Fatal("should not terminate after first turn")
	}

	// Turn that hits budget threshold (75%)
	tracker.RecordTurn(context.Background(), 650, 0, appendFn)
	if !tracker.ShouldTerminate() {
		t.Fatal("should terminate after budget threshold")
	}
}

func TestShouldTerminateAutoFalse(t *testing.T) {
	cfg := Config{
		MaxTokensPerConsult:        1000,
		AutoTerminateOnAuditComplete: false,
	}
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	// Turn that hits budget threshold
	tracker.RecordTurn(context.Background(), 800, 0, appendFn)
	if tracker.ShouldTerminate() {
		t.Fatal("should not terminate when auto-terminate is disabled")
	}
}

func TestStats(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	tracker.RecordTurn(context.Background(), 100, 1.5, appendFn)
	tracker.RecordTurn(context.Background(), 200, 2.5, appendFn)

	stats := tracker.Stats()
	if stats.TokensUsed != 300 {
		t.Fatalf("expected 300 tokens, got %d", stats.TokensUsed)
	}
	if stats.TurnCount != 2 {
		t.Fatalf("expected 2 turns, got %d", stats.TurnCount)
	}
	if stats.CostCents != 4.0 {
		t.Fatalf("expected 4.0 cost, got %f", stats.CostCents)
	}
}

func TestAuditHistory(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	tracker.RecordTurn(context.Background(), 100, 0, appendFn)
	tracker.RecordTurn(context.Background(), 200, 0, appendFn)

	history := tracker.AuditHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(history))
	}
	if history[0].TurnNumber != 1 || history[1].TurnNumber != 2 {
		t.Fatal("audit history order incorrect")
	}
}

func TestHardBudgetLimit(t *testing.T) {
	cfg := Config{MaxTokensPerConsult: 500}
	tracker := NewTracker(cfg, "c1", "s1", "main")

	var appended []string
	appendFn := func(ev eventlog.Event) error {
		appended = append(appended, ev.ID)
		return nil
	}

	result := tracker.RecordTurn(context.Background(), 500, 0, appendFn)
	if result.Decision != DecisionComplete {
		t.Fatalf("expected DecisionComplete when budget hit, got %s", result.Decision)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxTokensPerConsult != 16000 {
		t.Fatalf("expected 16000 max tokens, got %d", cfg.MaxTokensPerConsult)
	}
	if cfg.AutoTerminateOnAuditComplete {
		t.Fatal("expected auto-terminate to be false by default")
	}
	if cfg.AuditPrompt != DefaultAuditPrompt {
		t.Fatal("expected default audit prompt")
	}
}
