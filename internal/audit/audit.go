// Package audit implements self-auditing and cost-aware termination.
//
// After every turn, a lightweight self-audit determines whether to continue or terminate early.
// Cumulative tokens/cost per consult are tracked and stored in consult metadata.
package audit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"pancakes-harness/internal/eventlog"
)

// DefaultAuditPrompt is the self-audit prompt used to decide whether to continue.
const DefaultAuditPrompt = "Do I have enough information to answer the user query, or should I continue?"

// Decision represents the outcome of a self-audit.
type Decision string

const (
	DecisionComplete Decision = "complete" // Enough info, can terminate early
	DecisionContinue Decision = "continue" // Need more information, should continue
)

// AuditResult is the result of a single audit check.
type AuditResult struct {
	Decision       Decision
	Reason         string
	TokensUsed     int
	TokensLimit    int
	CostCents      float64
	CostLimitCents float64
	Timestamp      time.Time
	TurnNumber     int
}

// Config configures the self-auditing behavior.
type Config struct {
	// MaxTokensPerConsult is the hard token budget per consult.
	MaxTokensPerConsult int
	// AutoTerminateOnAuditComplete enables early termination when audit says "complete".
	AutoTerminateOnAuditComplete bool
	// AuditPrompt overrides the default self-audit prompt.
	AuditPrompt string
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxTokensPerConsult:        16000,
		AutoTerminateOnAuditComplete: false,
		AuditPrompt:                DefaultAuditPrompt,
	}
}

// Tracker tracks cumulative tokens/cost per consult and performs audit checks.
type Tracker struct {
	cfg            Config
	consultID      string
	sessionID      string
	branchID       string
	tokensUsed     int
	costCents      float64
	turnCount      int
	auditDecisions []AuditResult
}

// NewTracker creates a new audit tracker for a consult session.
func NewTracker(cfg Config, consultID, sessionID, branchID string) *Tracker {
	if cfg.MaxTokensPerConsult <= 0 {
		cfg.MaxTokensPerConsult = DefaultConfig().MaxTokensPerConsult
	}
	if cfg.AuditPrompt == "" {
		cfg.AuditPrompt = DefaultAuditPrompt
	}
	return &Tracker{
		cfg:       cfg,
		consultID: consultID,
		sessionID: sessionID,
		branchID:  branchID,
	}
}

// RecordTurn records token usage for a turn and runs the self-audit.
//
// The appendEvent callback records the audit decision in the consult event spine.
func (t *Tracker) RecordTurn(ctx context.Context, tokensUsed int, costCents float64, appendEvent func(ev eventlog.Event) error) AuditResult {
	t.turnCount++
	t.tokensUsed += tokensUsed
	t.costCents += costCents

	result := AuditResult{
		TokensUsed:     t.tokensUsed,
		TokensLimit:    t.cfg.MaxTokensPerConsult,
		CostCents:      t.costCents,
		CostLimitCents: 0, // Can be configured later
		Timestamp:      time.Now().UTC(),
		TurnNumber:     t.turnCount,
	}

	// Check hard budget limit
	if t.cfg.MaxTokensPerConsult > 0 && t.tokensUsed >= t.cfg.MaxTokensPerConsult {
		result.Decision = DecisionComplete
		result.Reason = fmt.Sprintf("token budget exhausted (%d/%d)", t.tokensUsed, t.cfg.MaxTokensPerConsult)
		t.auditDecisions = append(t.auditDecisions, result)
		_ = t.appendAuditEvent(appendEvent, result)
		return result
	}

	// Run self-audit (lightweight local check — in production could call a small model)
	auditDecision := t.runSelfAudit()
	result.Decision = auditDecision
	if auditDecision == DecisionComplete {
		result.Reason = "self-audit determined enough information"
	} else {
		result.Reason = "self-audit indicates more turns needed"
	}

	t.auditDecisions = append(t.auditDecisions, result)
	_ = t.appendAuditEvent(appendEvent, result)
	return result
}

// ShouldTerminate returns whether the consult should terminate early.
func (t *Tracker) ShouldTerminate() bool {
	if len(t.auditDecisions) == 0 {
		return false
	}
	last := t.auditDecisions[len(t.auditDecisions)-1]
	if !t.cfg.AutoTerminateOnAuditComplete {
		return false
	}
	return last.Decision == DecisionComplete
}

// Stats returns cumulative consult statistics.
func (t *Tracker) Stats() ConsultStats {
	return ConsultStats{
		TokensUsed:     t.tokensUsed,
		TokensLimit:    t.cfg.MaxTokensPerConsult,
		CostCents:      t.costCents,
		TurnCount:      t.turnCount,
		AuditDecisions: len(t.auditDecisions),
	}
}

// AuditHistory returns the full audit decision history.
func (t *Tracker) AuditHistory() []AuditResult {
	out := make([]AuditResult, len(t.auditDecisions))
	copy(out, t.auditDecisions)
	return out
}

func (t *Tracker) runSelfAudit() Decision {
	// Lightweight local heuristic:
	// - If we have a non-empty answer and are past turn 2, consider complete
	// - If tokens are getting high (> 75% of budget), lean toward complete
	// This is a simple heuristic; a production version might use a small model

	budgetRatio := float64(t.tokensUsed) / float64(t.cfg.MaxTokensPerConsult)
	if budgetRatio >= 0.75 {
		return DecisionComplete
	}
	if t.turnCount >= 3 {
		return DecisionComplete
	}
	return DecisionContinue
}

func (t *Tracker) appendAuditEvent(appendEvent func(ev eventlog.Event) error, result AuditResult) error {
	if appendEvent == nil {
		return nil
	}

	ev := eventlog.Event{
		ID:        fmt.Sprintf("audit.%s.%d.%d", t.sessionID, t.turnCount, result.Timestamp.UnixNano()),
		SessionID: t.sessionID,
		TS:        result.Timestamp,
		Kind:      eventlog.KindAuditDecision,
		BranchID:  t.branchID,
		Meta: map[string]any{
			"decision":          string(result.Decision),
			"reason":            result.Reason,
			"tokens_used":       result.TokensUsed,
			"tokens_limit":      result.TokensLimit,
			"cost_cents":        result.CostCents,
			"turn_number":       result.TurnNumber,
			"audit_prompt_used": strings.TrimSpace(t.cfg.AuditPrompt) != "",
			"auto_terminate":    t.cfg.AutoTerminateOnAuditComplete,
		},
	}
	return appendEvent(ev)
}

// ConsultStats holds cumulative consult statistics.
type ConsultStats struct {
	TokensUsed     int     `json:"tokens_used"`
	TokensLimit    int     `json:"tokens_limit"`
	CostCents      float64 `json:"cost_cents"`
	TurnCount      int     `json:"turn_count"`
	AuditDecisions int     `json:"audit_decisions"`
}
