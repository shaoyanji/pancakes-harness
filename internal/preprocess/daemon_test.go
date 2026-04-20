package preprocess

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
)

func TestParseExtraction_Valid(t *testing.T) {
	raw := `{
		"intent": "command",
		"intent_confidence": 0.91,
		"entities": [
			{"name": "pancakes-harness", "type": "project", "confidence": 0.95, "match_type": "exact"},
			{"name": "matt", "type": "person", "confidence": 0.88, "match_type": "exact"}
		],
		"topics": ["code", "debugging"],
		"sentiment": "neutral",
		"sentiment_confidence": 0.85,
		"summary": "Add preprocessing to harness",
		"flags": ["multi_intent"]
	}`

	ext, err := parseExtraction([]byte(raw))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if ext.SchemaVersion != SchemaVersionV1 {
		t.Errorf("schema_version = %v, want %v", ext.SchemaVersion, SchemaVersionV1)
	}
	if ext.Intent != IntentCommand {
		t.Errorf("intent = %v, want command", ext.Intent)
	}
	if ext.IntentConf != 0.91 {
		t.Errorf("intent_confidence = %v, want 0.91", ext.IntentConf)
	}
	if len(ext.Entities) != 2 {
		t.Fatalf("entities count = %d, want 2", len(ext.Entities))
	}
	if ext.Entities[0].Name != "pancakes-harness" {
		t.Errorf("entity[0].name = %v", ext.Entities[0].Name)
	}
	if ext.Entities[0].Type != EntityProject {
		t.Errorf("entity[0].type = %v", ext.Entities[0].Type)
	}
	if len(ext.Topics) != 2 {
		t.Fatalf("topics count = %d, want 2", len(ext.Topics))
	}
	if ext.Summary != "Add preprocessing to harness" {
		t.Errorf("summary = %v", ext.Summary)
	}
	if len(ext.Flags) != 1 || ext.Flags[0] != FlagMultiIntent {
		t.Errorf("flags = %v, want [multi_intent]", ext.Flags)
	}
	if ext.Timestamp.IsZero() {
		t.Error("timestamp should be set")
	}
}

func TestParseExtraction_MarkdownFences(t *testing.T) {
	raw := "```json\n{\"intent\": \"question\", \"intent_confidence\": 0.8, \"entities\": [], \"topics\": [\"general\"], \"sentiment\": \"neutral\", \"sentiment_confidence\": 0.7, \"summary\": \"test\"}\n```"

	ext, err := parseExtraction([]byte(raw))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if ext.Intent != IntentQuestion {
		t.Errorf("intent = %v, want question", ext.Intent)
	}
}

func TestParseExtraction_InvalidIntent(t *testing.T) {
	raw := `{
		"intent": "bananas",
		"intent_confidence": 0.9,
		"entities": [],
		"topics": [],
		"sentiment": "neutral",
		"sentiment_confidence": 0.7,
		"summary": "test"
	}`

	ext, err := parseExtraction([]byte(raw))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if ext.Intent != IntentUnknown {
		t.Errorf("intent = %v, want unknown", ext.Intent)
	}
	if !ext.HasFlag(FlagUncertain) {
		t.Error("expected uncertain flag for invalid intent")
	}
}

func TestParseExtraction_InvalidJSON(t *testing.T) {
	raw := `not json at all`
	_, err := parseExtraction([]byte(raw))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseExtraction_TruncatesSummary(t *testing.T) {
	longSummary := string(make([]byte, 300))
	for i := range longSummary {
		longSummary = longSummary[:i] + "x" + longSummary[i+1:]
	}
	// simpler approach
	longSummary = ""
	for i := 0; i < 300; i++ {
		longSummary += "x"
	}

	rawMap := map[string]any{
		"intent":              "question",
		"intent_confidence":   0.8,
		"entities":            []any{},
		"topics":              []string{"general"},
		"sentiment":           "neutral",
		"sentiment_confidence": 0.7,
		"summary":             longSummary,
	}
	data, _ := json.Marshal(rawMap)

	ext, err := parseExtraction(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(ext.Summary) > 200 {
		t.Errorf("summary length = %d, want <= 200", len(ext.Summary))
	}
}

func TestParseExtraction_SkipsEmptyEntityNames(t *testing.T) {
	raw := `{
		"intent": "command",
		"intent_confidence": 0.9,
		"entities": [
			{"name": "", "type": "tool", "confidence": 0.8, "match_type": "exact"},
			{"name": "grep", "type": "tool", "confidence": 0.9, "match_type": "exact"}
		],
		"topics": ["code"],
		"sentiment": "neutral",
		"sentiment_confidence": 0.7,
		"summary": "test"
	}`

	ext, err := parseExtraction([]byte(raw))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(ext.Entities) != 1 {
		t.Fatalf("entities count = %d, want 1 (empty name skipped)", len(ext.Entities))
	}
	if ext.Entities[0].Name != "grep" {
		t.Errorf("entity name = %v, want grep", ext.Entities[0].Name)
	}
}

// --- Mock types for daemon tests ---

type mockAdapter struct {
	response []byte
	err      error
	delay    time.Duration
}

func (m *mockAdapter) Name() string { return "mock-fast" }
func (m *mockAdapter) Call(ctx context.Context, prompt, text string) ([]byte, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.response, m.err
}

type mockEventLog struct {
	mu     sync.Mutex
	events []eventlog.Event
}

func (m *mockEventLog) AppendEvent(ctx context.Context, e eventlog.Event) error {
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
	return nil
}

func (m *mockEventLog) Events() []eventlog.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]eventlog.Event, len(m.events))
	copy(out, m.events)
	return out
}

func validExtractionJSON() []byte {
	return []byte(`{
		"intent": "command",
		"intent_confidence": 0.9,
		"entities": [{"name": "test", "type": "tool", "confidence": 0.8, "match_type": "exact"}],
		"topics": ["code"],
		"sentiment": "neutral",
		"sentiment_confidence": 0.7,
		"summary": "run tests"
	}`)
}

func TestDaemon_New(t *testing.T) {
	_, err := NewDaemon(Config{})
	if err == nil {
		t.Fatal("expected error for missing adapter")
	}

	adapter := &mockAdapter{response: validExtractionJSON()}
	_, err = NewDaemon(Config{FastAdapter: adapter})
	if err == nil {
		t.Fatal("expected error for missing event log")
	}

	d, err := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    &mockEventLog{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil {
		t.Fatal("daemon is nil")
	}
}

func TestDaemon_SubmitAndProcess(t *testing.T) {
	adapter := &mockAdapter{response: validExtractionJSON()}
	el := &mockEventLog{}
	var callbackCount int64

	d, _ := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    el,
		Timeout:     5 * time.Second,
		MaxWorkers:  1,
		QueueSize:   10,
		OnResult: func(r Result) {
			atomic.AddInt64(&callbackCount, 1)
		},
	})

	ctx := context.Background()
	d.Start(ctx)
	defer d.Stop()

	ok := d.Submit(Job{
		ID:        "test-1",
		SessionID: "session-1",
		BranchID:  "main",
		Text:      "run the tests",
		TS:        time.Now().UTC(),
	})
	if !ok {
		t.Fatal("submit returned false")
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	results := d.Results()
	if len(results) != 1 {
		t.Fatalf("results count = %d, want 1", len(results))
	}

	r := results[0]
	if !r.Succeeded() {
		t.Fatalf("result failed: %v", r.Error)
	}
	if r.Extraction.Intent != IntentCommand {
		t.Errorf("intent = %v, want command", r.Extraction.Intent)
	}
	if r.LatencyMs < 0 {
		t.Errorf("latency = %d, want >= 0", r.LatencyMs)
	}

	// Verify spine event
	events := el.Events()
	if len(events) != 1 {
		t.Fatalf("spine events = %d, want 1", len(events))
	}
	if events[0].Kind != eventlog.KindPreprocessExtract {
		t.Errorf("event kind = %v, want %v", events[0].Kind, eventlog.KindPreprocessExtract)
	}

	// Verify callback
	if atomic.LoadInt64(&callbackCount) != 1 {
		t.Errorf("callback count = %d, want 1", callbackCount)
	}

	// Verify stats
	stats := d.Stats()
	if stats.TotalJobs != 1 {
		t.Errorf("total jobs = %d, want 1", stats.TotalJobs)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("success count = %d, want 1", stats.SuccessCount)
	}
}

func TestDaemon_FailFast(t *testing.T) {
	adapter := &mockAdapter{err: errors.New("model unavailable")}
	el := &mockEventLog{}

	d, _ := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    el,
		Timeout:     1 * time.Second,
		MaxWorkers:  1,
	})

	ctx := context.Background()
	d.Start(ctx)
	defer d.Stop()

	d.Submit(Job{
		ID:        "fail-1",
		SessionID: "session-1",
		BranchID:  "main",
		Text:      "test",
		TS:        time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)

	results := d.Results()
	if len(results) != 1 {
		t.Fatalf("results count = %d, want 1", len(results))
	}
	if results[0].Succeeded() {
		t.Error("expected failed result")
	}

	// No spine event on failure
	events := el.Events()
	if len(events) != 0 {
		t.Errorf("spine events = %d, want 0 (fail-fast, no event on error)", len(events))
	}

	stats := d.Stats()
	if stats.ErrorCount != 1 {
		t.Errorf("error count = %d, want 1", stats.ErrorCount)
	}
}

func TestDaemon_Timeout(t *testing.T) {
	adapter := &mockAdapter{
		response: validExtractionJSON(),
		delay:    5 * time.Second, // longer than timeout
	}
	el := &mockEventLog{}

	d, _ := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    el,
		Timeout:     100 * time.Millisecond,
		MaxWorkers:  1,
	})

	ctx := context.Background()
	d.Start(ctx)
	defer d.Stop()

	d.Submit(Job{
		ID:        "timeout-1",
		SessionID: "session-1",
		BranchID:  "main",
		Text:      "test",
		TS:        time.Now().UTC(),
	})

	time.Sleep(500 * time.Millisecond)

	results := d.Results()
	if len(results) != 1 {
		t.Fatalf("results count = %d, want 1", len(results))
	}
	if results[0].Succeeded() {
		t.Error("expected timeout failure")
	}
}

func TestDaemon_QueueFull(t *testing.T) {
	// Adapter that blocks forever to fill the queue
	adapter := &mockAdapter{
		response: validExtractionJSON(),
		delay:    10 * time.Second,
	}
	el := &mockEventLog{}

	d, _ := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    el,
		Timeout:     30 * time.Second,
		MaxWorkers:  1,
		QueueSize:   2,
	})

	ctx := context.Background()
	d.Start(ctx)
	defer d.Stop()

	// Fill queue (worker is blocked, so queue fills up)
	d.Submit(Job{ID: "q1", Text: "a"})
	d.Submit(Job{ID: "q2", Text: "b"})

	// Third should fail
	ok := d.Submit(Job{ID: "q3", Text: "c"})
	if ok {
		t.Error("expected submit to fail when queue is full")
	}
}

func TestDaemon_SubmitAfterStop(t *testing.T) {
	adapter := &mockAdapter{response: validExtractionJSON()}
	el := &mockEventLog{}

	d, _ := NewDaemon(Config{
		FastAdapter: adapter,
		EventLog:    el,
		MaxWorkers:  1,
	})

	ctx := context.Background()
	d.Start(ctx)
	d.Stop()

	ok := d.Submit(Job{ID: "late", Text: "test"})
	if ok {
		t.Error("expected submit to fail after stop")
	}
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain json", "plain json"},
		{"```json\n{\"a\":1}\n```", "{\"a\":1}"},
		{"```\n{\"a\":1}\n```", "{\"a\":1}"},
		{"  ```json\n{\"a\":1}\n```  ", "{\"a\":1}"},
	}

	for _, tt := range tests {
		got := stripMarkdownFences(tt.input)
		if got != tt.want {
			t.Errorf("stripMarkdownFences(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
