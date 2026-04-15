package consultloop

import (
	"context"
	"errors"
	"testing"

	"pancakes-harness/internal/eventlog"
)

func TestExecuteSuccess(t *testing.T) {
	exec := NewExecutor(DefaultRecoveryConfig(), func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error) {
		return TurnOutcome{Answer: "done", Decision: "answer", AdapterName: adapter.Name()}, nil
	})

	var eventCount int
	outcome, err := exec.Execute(context.Background(), &mockAdapter{name: "primary"}, func(ev eventlog.Event) error {
		eventCount++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "done" {
		t.Fatalf("expected answer 'done', got %s", outcome.Answer)
	}
	if eventCount != 0 {
		t.Fatalf("expected no events, got %d", eventCount)
	}
}

func TestExecuteRetrySuccess(t *testing.T) {
	callCount := 0
	exec := NewExecutor(RecoveryConfig{MaxRetries: 1}, func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error) {
		callCount++
		if callCount == 1 {
			return TurnOutcome{}, errors.New("context_length exceeded")
		}
		return TurnOutcome{Answer: "done", Decision: "answer", AdapterName: adapter.Name()}, nil
	})

	var eventCount int
	outcome, err := exec.Execute(context.Background(), &mockAdapter{name: "primary"}, func(ev eventlog.Event) error {
		eventCount++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "done" {
		t.Fatalf("expected answer 'done', got %s", outcome.Answer)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
}

func TestExecuteFallbackSuccess(t *testing.T) {
	exec := NewExecutor(RecoveryConfig{
		MaxRetries:     1,
		FallbackModels: []ModelAdapter{&mockAdapter{name: "primary"}, &mockAdapter{name: "fallback"}},
	}, func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error) {
		if adapter.Name() == "primary" {
			return TurnOutcome{}, errors.New("context_length exceeded")
		}
		return TurnOutcome{Answer: "fallback done", Decision: "answer", AdapterName: adapter.Name()}, nil
	})

	var eventCount int
	outcome, err := exec.Execute(context.Background(), &mockAdapter{name: "primary"}, func(ev eventlog.Event) error {
		eventCount++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Answer != "fallback done" {
		t.Fatalf("expected 'fallback done', got %s", outcome.Answer)
	}
	if outcome.AdapterName != "fallback" {
		t.Fatalf("expected adapter 'fallback', got %s", outcome.AdapterName)
	}
}

func TestExecuteAllExhausted(t *testing.T) {
	exec := NewExecutor(RecoveryConfig{
		MaxRetries:     0,
		FallbackModels: []ModelAdapter{&mockAdapter{name: "p"}, &mockAdapter{name: "f"}},
	}, func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error) {
		return TurnOutcome{}, errors.New("context_length exceeded")
	})

	var eventCount int
	_, err := exec.Execute(context.Background(), &mockAdapter{name: "p"}, func(ev eventlog.Event) error {
		eventCount++
		return nil
	})
	if err == nil {
		t.Fatal("expected error when all models exhausted")
	}
}

func TestNonRecoverableErrorNoRetry(t *testing.T) {
	callCount := 0
	exec := NewExecutor(RecoveryConfig{MaxRetries: 1}, func(ctx context.Context, adapter ModelAdapter) (TurnOutcome, error) {
		callCount++
		return TurnOutcome{}, errors.New("invalid request")
	})

	var eventCount int
	_, err := exec.Execute(context.Background(), &mockAdapter{name: "primary"}, func(ev eventlog.Event) error {
		eventCount++
		return nil
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if callCount != 1 {
		t.Fatalf("expected 1 call (no retry for non-recoverable), got %d", callCount)
	}
}

func TestIsRecoverableError(t *testing.T) {
	recoverable := []string{
		"token budget exhausted",
		"context_length exceeded",
		"request timeout",
		"rate_limit reached",
		"service_unavailable",
		"content too long",
	}
	for _, msg := range recoverable {
		if !isRecoverableError(errors.New(msg)) {
			t.Fatalf("expected %q to be recoverable", msg)
		}
	}

	nonRecoverable := []string{
		"invalid request",
		"authentication failed",
		"model not found",
	}
	for _, msg := range nonRecoverable {
		if isRecoverableError(errors.New(msg)) {
			t.Fatalf("expected %q to be non-recoverable", msg)
		}
	}
}

type mockAdapter struct {
	name string
}

func (m *mockAdapter) Name() string {
	return m.name
}

func (m *mockAdapter) StatelessCall(ctx context.Context, req ModelRequest) ([]byte, error) {
	return nil, nil
}
