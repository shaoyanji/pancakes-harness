package tools

import (
	"context"
	"testing"
	"time"
)

func TestSuccessfulSubprocessToolCallReturnsStructuredResult(t *testing.T) {
	t.Parallel()

	runner := NewRunner(map[string]CommandSpec{
		"echo_tool": {
			Path: "sh",
			Args: []string{"-c", `cat >/dev/null; printf '{"ok":true,"call_id":"c1","result":{"value":"ok"},"summary":"done","artifacts":[]}'`},
		},
	})

	req := Request{
		Tool:      "echo_tool",
		CallID:    "c1",
		Args:      map[string]any{"x": "y"},
		TimeoutMS: 1000,
	}
	resp := runner.Run(context.Background(), req)
	if !resp.OK {
		t.Fatalf("expected success response, got %#v", resp)
	}
	if resp.CallID != "c1" {
		t.Fatalf("call id mismatch: %q", resp.CallID)
	}
	if resp.Summary != "done" {
		t.Fatalf("unexpected summary: %q", resp.Summary)
	}
	if got := resp.Result["value"]; got != "ok" {
		t.Fatalf("unexpected result value: %#v", got)
	}
}

func TestTimeoutBecomesNormalizedStructuredFailure(t *testing.T) {
	t.Parallel()

	runner := NewRunner(map[string]CommandSpec{
		"slow": {
			Path: "sh",
			Args: []string{"-c", `sleep 2; printf '{"ok":true,"call_id":"c2","result":{},"summary":"late","artifacts":[]}'`},
		},
	})
	req := Request{
		Tool:      "slow",
		CallID:    "c2",
		Args:      map[string]any{},
		TimeoutMS: 100,
	}

	resp := runner.Run(context.Background(), req)
	if resp.OK {
		t.Fatalf("expected timeout failure, got success: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Type != ErrorTypeTimeout {
		t.Fatalf("expected timeout error, got %#v", resp.Error)
	}
}

func TestMalformedToolJSONBecomesSchemaFailure(t *testing.T) {
	t.Parallel()

	runner := NewRunner(map[string]CommandSpec{
		"bad_json": {
			Path: "sh",
			Args: []string{"-c", `cat >/dev/null; printf 'not-json'`},
		},
	})
	req := Request{
		Tool:      "bad_json",
		CallID:    "c3",
		Args:      map[string]any{},
		TimeoutMS: 1000,
	}
	resp := runner.Run(context.Background(), req)
	if resp.OK {
		t.Fatalf("expected schema failure, got success: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Type != ErrorTypeSchema {
		t.Fatalf("expected schema failure type, got %#v", resp.Error)
	}
}

func TestNonZeroExitBecomesExecFailure(t *testing.T) {
	t.Parallel()

	runner := NewRunner(map[string]CommandSpec{
		"exit_fail": {
			Path: "sh",
			Args: []string{"-c", `echo boom 1>&2; exit 7`},
		},
	})
	req := Request{
		Tool:      "exit_fail",
		CallID:    "c4",
		Args:      map[string]any{},
		TimeoutMS: 1000,
	}

	resp := runner.Run(context.Background(), req)
	if resp.OK {
		t.Fatalf("expected exec failure, got success: %#v", resp)
	}
	if resp.Error == nil || resp.Error.Type != ErrorTypeExec {
		t.Fatalf("expected exec failure type, got %#v", resp.Error)
	}
}

func TestToolResultEventShapeIsPersistenceFriendly(t *testing.T) {
	t.Parallel()

	req := Request{
		Tool:      "tool_a",
		CallID:    "c5",
		Args:      map[string]any{"a": 1},
		TimeoutMS: 15000,
	}
	resp := Response{
		OK:      true,
		CallID:  "c5",
		Result:  map[string]any{"value": "x"},
		Summary: "short",
		Artifacts: []Artifact{
			{Name: "a1", BlobRef: "blob://b2"},
			{Name: "a2", BlobRef: "blob://b1"},
		},
	}

	ev := ToolResultEvent("e5", "s1", "main", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), req, resp)
	if ev.Kind != "tool.result" {
		t.Fatalf("unexpected kind: %q", ev.Kind)
	}
	if ev.BlobRef != "blob://b2" {
		t.Fatalf("expected first blob ref retained, got %q", ev.BlobRef)
	}
	if len(ev.Refs) != 2 || ev.Refs[0] != "blob://b1" || ev.Refs[1] != "blob://b2" {
		t.Fatalf("expected sorted refs, got %#v", ev.Refs)
	}
	if _, ok := ev.Meta["result_json"]; !ok {
		t.Fatalf("expected result_json in event metadata: %#v", ev.Meta)
	}
}
