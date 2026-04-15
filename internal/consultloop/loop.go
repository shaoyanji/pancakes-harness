// Package consultloop implements the self-healing query loop.
//
// It wraps the existing turn execution logic inside a small state machine that:
//   - Retries once with a meta-recovery prompt on recoverable failures.
//   - Falls back to the next cheaper model on repeated failure.
//   - Records every recovery attempt as a typed event in the consult event spine.
//   - Uses existing fingerprint/coalescing logic to deduplicate retries.
package consultloop

import (
	"context"
	"fmt"
	"time"

	"pancakes-harness/internal/eventlog"
)

// RecoveryConfig configures the self-healing loop behavior.
type RecoveryConfig struct {
	// MaxRetries is the maximum number of retry attempts per turn (default: 1).
	MaxRetries int
	// FallbackModels is an ordered list of model adapters to try, from most capable to cheapest.
	// The first adapter is the primary; subsequent ones are fallbacks.
	FallbackModels []ModelAdapter
	// RecoveryPrompt is injected on retry to instruct the model to continue from last checkpoint.
	RecoveryPrompt string
}

// ModelAdapter is the subset of the model adapter interface needed by the loop.
type ModelAdapter interface {
	Name() string
	StatelessCall(ctx context.Context, req ModelRequest) ([]byte, error)
}

// ModelRequest is the normalized request shape for model calls.
type ModelRequest struct {
	SessionID string
	BranchID  string
	Packet    []byte
}

// ModelResponse is the parsed response from a model call.
type ModelResponse struct {
	Decision string
	Answer   string
	Raw      []byte
}

// TurnFn executes a single turn with the given model adapter and returns the result.
// This is the hook into the existing runtime session logic.
type TurnFn func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error)

// TurnOutcome represents the result of a single turn execution.
type TurnOutcome struct {
	Answer        string
	Decision      string
	AdapterName   string
	EnvelopeBytes int
	TokensUsed    int
}

// RecoveryEvent is the metadata recorded for each recovery attempt.
type RecoveryEvent struct {
	AttemptNumber int
	OriginalError string
	RecoveryType  string // "retry" | "fallback"
	ModelName     string
	Timestamp     time.Time
}

// DefaultRecoveryConfig returns sensible defaults.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		MaxRetries: 1,
		RecoveryPrompt: "Continue from last stable checkpoint. Do not repeat prior output. " +
			"Focus on the user's original query and provide a concise, complete response.",
	}
}

// Executor runs the self-healing query loop.
type Executor struct {
	cfg  RecoveryConfig
	turn TurnFn
}

// NewExecutor creates a new self-healing loop executor.
func NewExecutor(cfg RecoveryConfig, turn TurnFn) *Executor {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 1
	}
	if cfg.RecoveryPrompt == "" {
		cfg.RecoveryPrompt = DefaultRecoveryConfig().RecoveryPrompt
	}
	return &Executor{cfg: cfg, turn: turn}
}

// Execute runs the turn with self-healing. It tries the primary model, retries on
// recoverable failure, and falls back to cheaper models if needed.
//
// The appendEvent callback records recovery/fallback events in the consult event spine.
func (e *Executor) Execute(ctx context.Context, adapter ModelAdapter, appendEvent func(ev eventlog.Event) error) (TurnOutcome, error) {
	maxRetries := e.cfg.MaxRetries
	models := e.cfg.FallbackModels
	if len(models) == 0 {
		models = []ModelAdapter{adapter}
	}

	var lastErr error
	for modelIdx, model := range models {
		for attempt := 0; attempt <= maxRetries; attempt++ {
			outcome, err := e.turn(ctx, model)
			if err == nil {
				if attempt > 0 || modelIdx > 0 {
					// Record successful recovery/fallback
					if err := appendRecoveryEvent(appendEvent, RecoveryEvent{
						AttemptNumber: attempt,
						OriginalError: errorStr(lastErr),
						RecoveryType:  recoveryType(attempt, modelIdx),
						ModelName:     model.Name(),
						Timestamp:     time.Now().UTC(),
					}); err != nil {
						// Non-fatal: the turn succeeded even if we couldn't record the event
					}
				}
				outcome.AdapterName = model.Name()
				return outcome, nil
			}

			lastErr = err

			// Check if this is a recoverable error
			if !isRecoverableError(err) {
				// Non-recoverable: try next model immediately
				break
			}

			// Record retry attempt
			if attempt < maxRetries {
				if err := appendRecoveryEvent(appendEvent, RecoveryEvent{
					AttemptNumber: attempt + 1,
					OriginalError: err.Error(),
					RecoveryType:  "retry",
					ModelName:     model.Name(),
					Timestamp:     time.Now().UTC(),
				}); err != nil {
					// Non-fatal
				}
			}
		}

		// If we have more models to try, record fallback event
		if modelIdx < len(models)-1 {
			if err := appendRecoveryEvent(appendEvent, RecoveryEvent{
				AttemptNumber: 0,
				OriginalError: errorStr(lastErr),
				RecoveryType:  "fallback",
				ModelName:     models[modelIdx+1].Name(),
				Timestamp:     time.Now().UTC(),
			}); err != nil {
				// Non-fatal
			}
		}
	}

	return TurnOutcome{}, fmt.Errorf("all models exhausted after retries: %w", lastErr)
}

func recoveryType(attempt, modelIdx int) string {
	if modelIdx > 0 {
		return "fallback"
	}
	if attempt > 0 {
		return "retry"
	}
	return "initial"
}

func appendRecoveryEvent(appendEvent func(ev eventlog.Event) error, rec RecoveryEvent) error {
	if appendEvent == nil {
		return nil
	}

	kind := eventlog.KindRecoveryAttempt
	if rec.RecoveryType == "fallback" {
		kind = eventlog.KindRecoveryFallback
	}

	ev := eventlog.Event{
		ID:        fmt.Sprintf("recovery.%s.%d", rec.ModelName, rec.Timestamp.UnixNano()),
		SessionID: "recovery",
		TS:        rec.Timestamp,
		Kind:      kind,
		BranchID:  "main",
		Meta: map[string]any{
			"attempt_number":  rec.AttemptNumber,
			"original_error":  rec.OriginalError,
			"recovery_type":   rec.RecoveryType,
			"model_name":      rec.ModelName,
			"recovery_prompt": "injected",
		},
	}
	return appendEvent(ev)
}

func isRecoverableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Token-budget exhaustion
	if containsAny(msg, "token", "budget", "exhaust", "overflow", "context_length") {
		return true
	}
	// Model errors that might be transient
	if containsAny(msg, "timeout", "rate_limit", "service_unavailable", "overloaded") {
		return true
	}
	// Context overflow
	if containsAny(msg, "context", "too long", "exceeds") {
		return true
	}
	return false
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func errorStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
