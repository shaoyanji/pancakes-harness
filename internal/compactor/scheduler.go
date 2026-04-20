package compactor

import (
	"sync"
	"time"
)

// ScheduleConfig controls when automatic compaction fires.
type ScheduleConfig struct {
	// TriggerTurns fires compaction every N completed turns.
	// 0 = disabled (rely on budget pressure only).
	TriggerTurns int

	// BudgetPressureRatio triggers compaction when the packet assembler
	// uses this fraction of the max envelope budget (0.0–1.0).
	// 0 = disabled (rely on turn count only).
	BudgetPressureRatio float64

	// MinEvents ensures compaction doesn't fire below this event count.
	// Avoids pointless compaction on tiny histories.
	MinEvents int

	// CooldownTurns prevents compaction from firing again within N turns
	// after the last compaction. Prevents compaction storms.
	CooldownTurns int
}

// DefaultScheduleConfig returns sensible defaults for production use.
func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		TriggerTurns:        10,
		BudgetPressureRatio: 0.8,
		MinEvents:           20,
		CooldownTurns:       3,
	}
}

// Scheduler tracks turn count, budget pressure, and compaction history
// to decide when to trigger a compaction pass.
type Scheduler struct {
	cfg ScheduleConfig
	mu  sync.Mutex

	// Counters
	totalTurns          int
	turnsSinceCompact   int
	turnsSinceLastFire  int // for cooldown
	eventCount          int
	lastBudgetRatio     float64
	lastCompactionTime  time.Time

	// Stats
	totalFirings    int
	totalSuppressed int
}

// NewScheduler creates a compaction trigger tracker.
func NewScheduler(cfg ScheduleConfig) *Scheduler {
	if cfg.MinEvents <= 0 {
		cfg.MinEvents = 20
	}
	return &Scheduler{cfg: cfg}
}

// RecordTurn updates the scheduler after a completed user turn.
func (s *Scheduler) RecordTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalTurns++
	s.turnsSinceCompact++
	s.turnsSinceLastFire++
}

// RecordBudgetPressure reports the current budget usage ratio from the assembler.
func (s *Scheduler) RecordBudgetPressure(ratio float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBudgetRatio = ratio
}

// SetEventCount updates the total event count (from the event spine).
func (s *Scheduler) SetEventCount(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventCount = n
}

// ShouldCompact returns true if conditions are met for a compaction pass.
func (s *Scheduler) ShouldCompact() (should bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Too few events — skip
	if s.eventCount < s.cfg.MinEvents {
		return false, ""
	}

	// Cooldown — skip (only after at least one compaction has fired)
	if s.totalFirings > 0 && s.cfg.CooldownTurns > 0 && s.turnsSinceLastFire < s.cfg.CooldownTurns {
		s.totalSuppressed++
		return false, ""
	}

	// Turn count trigger
	if s.cfg.TriggerTurns > 0 && s.turnsSinceCompact >= s.cfg.TriggerTurns {
		return true, "turn_count"
	}

	// Budget pressure trigger
	if s.cfg.BudgetPressureRatio > 0 && s.lastBudgetRatio >= s.cfg.BudgetPressureRatio {
		return true, "budget_pressure"
	}

	return false, ""
}

// MarkCompacted resets counters after a successful compaction pass.
func (s *Scheduler) MarkCompacted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnsSinceCompact = 0
	s.turnsSinceLastFire = 0
	s.lastCompactionTime = time.Now()
	s.totalFirings++
}

// MarkCompactionFailed clears cooldown on failure so the next ShouldCompact passes.
func (s *Scheduler) MarkCompactionFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Don't reset turnsSinceCompact — we still want compaction.
	// Set cooldown to threshold so the next check passes immediately.
	s.turnsSinceLastFire = s.cfg.CooldownTurns
}

// Stats returns the current scheduler state for observability.
type ScheduleStats struct {
	TotalTurns          int     `json:"total_turns"`
	TurnsSinceCompact   int     `json:"turns_since_compact"`
	EventCount          int     `json:"event_count"`
	LastBudgetRatio     float64 `json:"last_budget_ratio"`
	LastCompactionTime  string  `json:"last_compaction_time,omitempty"`
	TotalFirings        int     `json:"total_firings"`
	TotalSuppressed     int     `json:"total_suppressed"`
}

func (s *Scheduler) Stats() ScheduleStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := ScheduleStats{
		TotalTurns:        s.totalTurns,
		TurnsSinceCompact: s.turnsSinceCompact,
		EventCount:        s.eventCount,
		LastBudgetRatio:   s.lastBudgetRatio,
		TotalFirings:      s.totalFirings,
		TotalSuppressed:   s.totalSuppressed,
	}
	if !s.lastCompactionTime.IsZero() {
		stats.LastCompactionTime = s.lastCompactionTime.Format(time.RFC3339)
	}
	return stats
}
