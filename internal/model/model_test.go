package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
)

func TestMalformedModelResponseRejectedCleanly(t *testing.T) {
	t.Parallel()

	b := backend.NewMemoryBackend()
	adapter := MockAdapter{
		NameValue: "mock-bad",
		CallFunc: func(ctx context.Context, req Request) ([]byte, error) {
			return []byte(`{"answer":"missing decision"}`), nil
		},
	}
	req := Request{
		SessionID: "s1",
		BranchID:  "main",
		Packet:    []byte(`{"x":1}`),
	}

	_, err := ExecuteAndPersist(context.Background(), b, adapter, req, "m1", time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrMalformedModelResponse) {
		t.Fatalf("expected malformed response error, got %v", err)
	}

	events, listErr := b.ListEventsBySession(context.Background(), "s1")
	if listErr != nil {
		t.Fatalf("list events: %v", listErr)
	}
	if len(events) != 1 {
		t.Fatalf("expected one invalid-schema event, got %d", len(events))
	}
	if events[0].Kind != eventlog.KindResponseInvalid {
		t.Fatalf("expected %q, got %q", eventlog.KindResponseInvalid, events[0].Kind)
	}
}

func TestValidStructuredModelResponsePassesValidation(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"decision":"tool_calls",
		"tool_calls":[{"tool":"search","call_id":"c1","args":{"q":"hello"}}],
		"unresolved_refs":["r1"]
	}`)
	resp, err := ParseAndValidateResponse(raw)
	if err != nil {
		t.Fatalf("validate response: %v", err)
	}
	if resp.Decision != "tool_calls" {
		t.Fatalf("unexpected decision: %q", resp.Decision)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Tool != "search" {
		t.Fatalf("unexpected tool calls: %#v", resp.ToolCalls)
	}
}

func TestMockAdapterCanBeSwappedWithoutRuntimeChanges(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, a Adapter) Response {
		t.Helper()
		result, err := Execute(context.Background(), a, Request{
			SessionID: "s-swap",
			BranchID:  "main",
			Packet:    []byte(`{"packet":"tiny"}`),
		})
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return result.Response
	}

	mockAdapter := MockAdapter{
		NameValue: "mock-good",
		CallFunc: func(ctx context.Context, req Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"ok"}`), nil
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"answer","answer":"ok"}`))
	}))
	defer server.Close()

	httpAdapter := NewHTTPAdapter(HTTPConfig{Endpoint: server.URL})

	mockResp := run(t, mockAdapter)
	httpResp := run(t, httpAdapter)

	if mockResp.Decision != httpResp.Decision || mockResp.Answer != httpResp.Answer {
		t.Fatalf("swapped adapters should produce same runtime-facing result: mock=%#v http=%#v", mockResp, httpResp)
	}
}

func TestLongConversationStillResultsInTinyOutboundPackets(t *testing.T) {
	t.Parallel()

	longItem := strings.Repeat("conversation-chunk-", 30)
	totalLocalBytes := 0
	working := make([]assembler.WorkingItem, 0, 350)
	for i := 0; i < 350; i++ {
		totalLocalBytes += len(longItem)
		working = append(working, assembler.WorkingItem{
			ID:              "turn-" + strings.Repeat("x", i%7+1) + string(rune('a'+(i%26))),
			Kind:            "turn.user",
			Text:            longItem,
			SummaryRef:      "summary://turn/" + string(rune('a'+(i%26))),
			BlobRef:         "blob://turn/" + string(rune('a'+(i%26))),
			FrontierOrdinal: i,
		})
	}

	packet, err := assembler.Assemble(assembler.Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []assembler.Header{
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: assembler.PacketBody{
			SessionID:            "s-long",
			BranchHandle:         "b:main",
			CheckpointSummaryRef: "summary://checkpoint/long",
			WorkingSet:           working,
		},
	})
	if err != nil {
		t.Fatalf("assemble long conversation: %v", err)
	}
	if packet.Measurement.EnvelopeBytes > assembler.MaxEnvelopeBytes {
		t.Fatalf("packet exceeded hard cap: %d", packet.Measurement.EnvelopeBytes)
	}
	if totalLocalBytes <= len(packet.BodyJSON) {
		t.Fatalf("expected outbound packet to be smaller than local conversation: local=%d outbound=%d", totalLocalBytes, len(packet.BodyJSON))
	}
	if len(packet.BodyJSON) > 4096 {
		t.Fatalf("expected tiny outbound packet, got %d bytes", len(packet.BodyJSON))
	}

	capturedLen := 0
	mock := MockAdapter{
		NameValue: "mock-capture",
		CallFunc: func(ctx context.Context, req Request) ([]byte, error) {
			capturedLen = len(req.Packet)
			return []byte(`{"decision":"continue"}`), nil
		},
	}
	_, execErr := Execute(context.Background(), mock, Request{
		SessionID: "s-long",
		BranchID:  "main",
		Packet:    packet.BodyJSON,
	})
	if execErr != nil {
		t.Fatalf("model execute: %v", execErr)
	}
	if capturedLen != len(packet.BodyJSON) {
		t.Fatalf("adapter should receive assembler output unchanged, got %d want %d", capturedLen, len(packet.BodyJSON))
	}
}
