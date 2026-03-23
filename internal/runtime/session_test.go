package runtime

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/backend/xs"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/tools"
)

func TestEndToEndLocalSessionValidModelResponse(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	m := model.MockAdapter{
		NameValue: "mock",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"hello from agent"}`), nil
		},
	}
	s, err := StartSession(Config{
		SessionID:    "s-e2e-answer",
		Backend:      mem,
		ModelAdapter: m,
		ToolRunner:   tools.NewRunner(nil),
		ModelHeaders: []assembler.Header{{Name: "Content-Type", Value: "application/json"}},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	res, err := s.HandleUserTurn(context.Background(), "main", "hi")
	if err != nil {
		t.Fatalf("handle turn: %v", err)
	}
	if res.Answer != "hello from agent" {
		t.Fatalf("unexpected answer: %q", res.Answer)
	}

	events, err := mem.ListEventsBySession(context.Background(), "s-e2e-answer")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected persisted events")
	}
	if !containsKind(events, eventlog.KindTurnUser) || !containsKind(events, eventlog.KindTurnAgent) {
		t.Fatalf("expected persisted user+agent turns, got %#v", kinds(events))
	}
}

func TestEndToEndToolCallFlowPersistsToolEvents(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	step := 0
	m := model.MockAdapter{
		NameValue: "mock-tools",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			step++
			if step == 1 {
				return []byte(`{"decision":"tool_calls","tool_calls":[{"tool":"echo_tool","call_id":"c1","args":{"q":"weather"}}]}`), nil
			}
			if !strings.Contains(string(req.Packet), "summary://tool/c1") {
				return []byte(`{"decision":"answer","answer":"missing followup summary ref"}`), nil
			}
			return []byte(`{"decision":"answer","answer":"tool integrated"}`), nil
		},
	}

	runner := tools.NewRunner(map[string]tools.CommandSpec{
		"echo_tool": {
			Path: "sh",
			Args: []string{"-c", `cat >/dev/null; printf '{"ok":true,"call_id":"c1","result":{"payload":"big output"},"summary":"tool summary","artifacts":[]}'`},
		},
	})
	s, err := StartSession(Config{
		SessionID:    "s-e2e-tool",
		Backend:      mem,
		ModelAdapter: m,
		ToolRunner:   runner,
		ModelHeaders: []assembler.Header{{Name: "Content-Type", Value: "application/json"}},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	res, err := s.HandleUserTurn(context.Background(), "main", "use tool please")
	if err != nil {
		t.Fatalf("handle turn: %v", err)
	}
	if res.Answer != "tool integrated" {
		t.Fatalf("unexpected answer: %q", res.Answer)
	}
	events, err := mem.ListEventsBySession(context.Background(), "s-e2e-tool")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !containsKind(events, eventlog.KindToolRequest) || !containsKind(events, eventlog.KindToolResult) {
		t.Fatalf("expected tool events persisted, got %#v", kinds(events))
	}
}

func TestReplayRebuildsSessionStateAfterSeveralTurns(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	m := model.MockAdapter{
		NameValue: "mock-replay",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"ok"}`), nil
		},
	}
	s, err := StartSession(Config{
		SessionID:    "s-replay-runtime",
		Backend:      mem,
		ModelAdapter: m,
		ToolRunner:   tools.NewRunner(nil),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, _ = s.HandleUserTurn(context.Background(), "main", "first")
	if err := s.ForkBranch(context.Background(), "main", "alt"); err != nil {
		t.Fatalf("fork branch: %v", err)
	}
	_, _ = s.HandleUserTurn(context.Background(), "alt", "branch turn")
	_, _ = s.HandleUserTurn(context.Background(), "main", "second")

	replayed, err := s.ReplaySession(context.Background())
	if err != nil {
		t.Fatalf("replay session: %v", err)
	}
	if replayed.SessionState.EventCount < 6 {
		t.Fatalf("expected multiple persisted events, got %d", replayed.SessionState.EventCount)
	}
	if replayed.Branches["main"] == "" || replayed.Branches["alt"] == "" {
		t.Fatalf("expected replayed branch heads for main+alt, got %#v", replayed.Branches)
	}
}

func TestOutboundModelPacketsRemainUnderBudgetInLongConversationFlow(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	m := model.MockAdapter{
		NameValue: "mock-long",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"ok"}`), nil
		},
	}
	s, err := StartSession(Config{
		SessionID:    "s-long-runtime",
		Backend:      mem,
		ModelAdapter: m,
		ToolRunner:   tools.NewRunner(nil),
		ModelHeaders: []assembler.Header{{Name: "Content-Type", Value: "application/json"}},
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	longTurn := strings.Repeat("local-memory-chunk-", 120)
	for i := 0; i < 12; i++ {
		if _, err := s.HandleUserTurn(context.Background(), "main", longTurn); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	events, err := mem.ListEventsBySession(context.Background(), "s-long-runtime")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	candidates := filterKind(events, eventlog.KindPacketCandidate)
	if len(candidates) == 0 {
		t.Fatal("expected packet candidate events")
	}
	for _, e := range candidates {
		val := readMetaString(e.Meta, "envelope_bytes")
		n, convErr := strconv.Atoi(val)
		if convErr != nil {
			t.Fatalf("bad envelope_bytes value %q: %v", val, convErr)
		}
		if n > assembler.MaxEnvelopeBytes {
			t.Fatalf("packet candidate exceeded budget: %d", n)
		}
	}
}

func TestMalformedModelResponseDoesNotCorruptRuntimeState(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	m := model.MockAdapter{
		NameValue: "mock-bad",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"answer":"missing decision"}`), nil
		},
	}
	s, err := StartSession(Config{
		SessionID:    "s-malformed-runtime",
		Backend:      mem,
		ModelAdapter: m,
		ToolRunner:   tools.NewRunner(nil),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	_, err = s.HandleUserTurn(context.Background(), "main", "hi")
	if !errors.Is(err, model.ErrMalformedModelResponse) {
		t.Fatalf("expected malformed response error, got %v", err)
	}

	events, err := mem.ListEventsBySession(context.Background(), "s-malformed-runtime")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if !containsKind(events, eventlog.KindResponseInvalid) {
		t.Fatalf("expected response.invalid_schema event, got %#v", kinds(events))
	}
	if containsKind(events, eventlog.KindTurnAgent) {
		t.Fatalf("agent turn must not be persisted on malformed model response, got %#v", kinds(events))
	}
}

func TestBackendSwapDoesNotChangeRuntimeBehavior(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, b backend.Backend, sessionID string) string {
		t.Helper()
		m := model.MockAdapter{
			NameValue: "mock-swap",
			CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
				return []byte(`{"decision":"answer","answer":"consistent"}`), nil
			},
		}
		s, err := StartSession(Config{
			SessionID:    sessionID,
			Backend:      b,
			ModelAdapter: m,
			ToolRunner:   tools.NewRunner(nil),
		})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		res, err := s.HandleUserTurn(context.Background(), "main", "hello")
		if err != nil {
			t.Fatalf("handle turn: %v", err)
		}
		return res.Answer
	}

	mem := backend.NewMemoryBackend()
	xsBackend := xs.NewAdapter(
		xs.Config{Command: "sh", HealthArgs: []string{"-c", "echo ok"}},
		xs.WithCommandRunner(func(ctx context.Context, command string, args ...string) ([]byte, error) {
			return []byte("ok"), nil
		}),
	)

	a := run(t, mem, "s-swap-mem")
	b := run(t, xsBackend, "s-swap-xs")
	if a != b {
		t.Fatalf("expected equal runtime-facing behavior across backends: %q vs %q", a, b)
	}
}

func containsKind(events []eventlog.Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func filterKind(events []eventlog.Event, kind string) []eventlog.Event {
	out := make([]eventlog.Event, 0)
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func kinds(events []eventlog.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}
