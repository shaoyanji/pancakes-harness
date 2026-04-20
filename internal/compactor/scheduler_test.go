package compactor

import "testing"

func TestSchedulerTurnCountTrigger(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns: 5,
		MinEvents:    10,
		CooldownTurns: 0,
	})
	s.SetEventCount(50)

	// Should not fire yet
	should, _ := s.ShouldCompact()
	if should {
		t.Fatal("should not compact before trigger turns")
	}

	// Record 5 turns
	for i := 0; i < 5; i++ {
		s.RecordTurn()
	}

	should, reason := s.ShouldCompact()
	if !should {
		t.Fatal("should compact after 5 turns")
	}
	if reason != "turn_count" {
		t.Fatalf("expected reason 'turn_count', got %q", reason)
	}

	// After compaction, resets
	s.MarkCompacted()
	should, _ = s.ShouldCompact()
	if should {
		t.Fatal("should not compact immediately after compaction")
	}
}

func TestSchedulerBudgetPressureTrigger(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns:        0, // disable turn count
		BudgetPressureRatio: 0.8,
		MinEvents:           10,
	})
	s.SetEventCount(50)

	s.RecordBudgetPressure(0.5)
	should, _ := s.ShouldCompact()
	if should {
		t.Fatal("should not compact at 0.5 pressure")
	}

	s.RecordBudgetPressure(0.85)
	should, reason := s.ShouldCompact()
	if !should {
		t.Fatal("should compact at 0.85 pressure")
	}
	if reason != "budget_pressure" {
		t.Fatalf("expected reason 'budget_pressure', got %q", reason)
	}
}

func TestSchedulerMinEvents(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns: 3,
		MinEvents:    50,
	})
	s.SetEventCount(10) // below threshold

	for i := 0; i < 5; i++ {
		s.RecordTurn()
	}

	should, _ := s.ShouldCompact()
	if should {
		t.Fatal("should not compact below MinEvents")
	}
}

func TestSchedulerCooldown(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns: 2,
		MinEvents:    5,
		CooldownTurns: 3,
	})
	s.SetEventCount(50)

	// Trigger first compaction
	s.RecordTurn()
	s.RecordTurn()
	should, _ := s.ShouldCompact()
	if !should {
		t.Fatal("should compact")
	}
	s.MarkCompacted()

	// Immediately after — cooldown blocks
	should, _ = s.ShouldCompact()
	if should {
		t.Fatal("cooldown should block immediate re-compaction")
	}

	// After cooldown turns
	s.RecordTurn()
	s.RecordTurn()
	s.RecordTurn()
	should, _ = s.ShouldCompact()
	if !should {
		t.Fatal("should compact after cooldown expires")
	}
}

func TestSchedulerStats(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns: 5,
		MinEvents:    5,
	})
	s.SetEventCount(50)

	for i := 0; i < 10; i++ {
		s.RecordTurn()
	}

	stats := s.Stats()
	if stats.TotalTurns != 10 {
		t.Fatalf("expected 10 total turns, got %d", stats.TotalTurns)
	}
	if stats.TurnsSinceCompact != 10 {
		t.Fatalf("expected 10 turns since compact, got %d", stats.TurnsSinceCompact)
	}
	if stats.EventCount != 50 {
		t.Fatalf("expected 50 events, got %d", stats.EventCount)
	}
}

func TestSchedulerMarkCompactionFailed(t *testing.T) {
	s := NewScheduler(ScheduleConfig{
		TriggerTurns: 2,
		MinEvents:    5,
		CooldownTurns: 3,
	})
	s.SetEventCount(50)

	// Trigger compaction
	s.RecordTurn()
	s.RecordTurn()
	should, _ := s.ShouldCompact()
	if !should {
		t.Fatal("should compact")
	}

	// Failure resets cooldown but doesn't reset turn count
	s.MarkCompactionFailed()

	// Should still be able to trigger immediately (cooldown cleared)
	should, _ = s.ShouldCompact()
	if !should {
		t.Fatal("should retry after failure clears cooldown")
	}
}

func TestSchedulerDefaultConfig(t *testing.T) {
	cfg := DefaultScheduleConfig()
	if cfg.TriggerTurns != 10 {
		t.Fatalf("expected 10 trigger turns, got %d", cfg.TriggerTurns)
	}
	if cfg.BudgetPressureRatio != 0.8 {
		t.Fatalf("expected 0.8 pressure ratio, got %.1f", cfg.BudgetPressureRatio)
	}
	if cfg.MinEvents != 20 {
		t.Fatalf("expected 20 min events, got %d", cfg.MinEvents)
	}
	if cfg.CooldownTurns != 3 {
		t.Fatalf("expected 3 cooldown turns, got %d", cfg.CooldownTurns)
	}
}
