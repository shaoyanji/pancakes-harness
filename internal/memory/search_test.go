package memory

import (
	"testing"

	"pancakes-harness/internal/eventlog"
)

// --- Tokenizer tests ---

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"CompactByScore works well", []string{"compactbyscore", "works", "well"}},
		{"a bc de", []string{"bc", "de"}}, // "bc" and "de" are 2 chars (>= 2 threshold)
		{"", nil},
		{"  multiple   spaces  ", []string{"multiple", "spaces"}},
		{"snake_case_func", []string{"snake_case_func"}},
		{"mixedCase and_underscore", []string{"mixedcase", "and_underscore"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

// --- AST extraction tests ---

func TestExtractSymbols_Function(t *testing.T) {
	src := `package memory

func CompactByScore(events []Event, scores map[string]float64) []Event {
	return nil
}
`
	symbols, err := extractSymbols(src)
	if err != nil {
		t.Fatalf("extractSymbols failed: %v", err)
	}

	want := map[string]bool{
		"memory":         true,
		"CompactByScore": true,
		"Event":          true, // from param and return types
		"float64":        true,
	}
	for _, s := range symbols {
		delete(want, s)
	}
	if len(want) > 0 {
		t.Errorf("missing symbols: %v (got: %v)", want, symbols)
	}
}

func TestExtractSymbols_Method(t *testing.T) {
	src := `package memory

func (m *Manager) IndexEvent(ev Event) {
}

func (idx *Index) Search(query string) []Result {
	return nil
}
`
	symbols, err := extractSymbols(src)
	if err != nil {
		t.Fatalf("extractSymbols failed: %v", err)
	}

	want := map[string]bool{
		"Manager":    true, // receiver type
		"IndexEvent": true,
		"Index":      true, // receiver type
		"Search":     true,
		"Result":     true, // return type
	}
	for _, s := range symbols {
		delete(want, s)
	}
	if len(want) > 0 {
		t.Errorf("missing symbols: %v (got: %v)", want, symbols)
	}
}

func TestExtractSymbols_TypeDecl(t *testing.T) {
	src := `package eventlog

import "time"

type Event struct {
	ID        string
	SessionID string
	TS        time.Time
	Kind      string
}
`
	symbols, err := extractSymbols(src)
	if err != nil {
		t.Fatalf("extractSymbols failed: %v", err)
	}

	want := map[string]bool{
		"eventlog":  true,
		"Event":     true,
		"ID":        true,
		"SessionID": true,
		"time":      true,
		"Kind":      true,
	}
	for _, s := range symbols {
		delete(want, s)
	}
	if len(want) > 0 {
		t.Errorf("missing symbols: %v (got: %v)", want, symbols)
	}
}

func TestExtractSymbols_Interface(t *testing.T) {
	src := `package backend

type Backend interface {
	AppendEvent(event Event) error
	ReadEvent(id string) (Event, error)
	HealthCheck() error
}
`
	symbols, err := extractSymbols(src)
	if err != nil {
		t.Fatalf("extractSymbols failed: %v", err)
	}

	want := map[string]bool{
		"backend":      true,
		"Backend":      true,
		"AppendEvent":  true,
		"ReadEvent":    true,
		"HealthCheck":  true,
	}
	for _, s := range symbols {
		delete(want, s)
	}
	if len(want) > 0 {
		t.Errorf("missing symbols: %v (got: %v)", want, symbols)
	}
}

func TestExtractSymbols_NonGo(t *testing.T) {
	src := "this is not go code at all, just plain text"
	symbols, err := extractSymbols(src)
	if err == nil {
		// Plain text may or may not parse — either way, we shouldn't crash
		t.Logf("parse unexpectedly succeeded, got symbols: %v", symbols)
	}
}

// --- Index tests ---

func makeEvent(id, branchID, text string) eventlog.Event {
	return eventlog.Event{
		ID:       id,
		BranchID: branchID,
		Kind:     "turn.user",
		Meta:     map[string]any{"text": text},
	}
}

func TestIndex_AddAndSearch(t *testing.T) {
	idx := NewIndex()

	idx.Add(makeEvent("ev1", "main", "how does context compaction work in the memory system"))
	idx.Add(makeEvent("ev2", "main", "branch forking creates a new pointer-based branch"))
	idx.Add(makeEvent("ev3", "main", "compaction removes low scored events from the event spine"))

	results := idx.Search("compaction", SearchOpts{})
	if len(results) == 0 {
		t.Fatal("expected results for 'compaction'")
	}
	// ev1 and ev3 both mention compaction; ev3 has it more prominently
	topIDs := make([]string, len(results))
	for i, r := range results {
		topIDs[i] = r.EventID
	}
	t.Logf("search 'compaction': %v", topIDs)

	// The top result should be ev1 or ev3 (both relevant)
	if results[0].EventID != "ev1" && results[0].EventID != "ev3" {
		t.Errorf("top result = %q, want ev1 or ev3", results[0].EventID)
	}
}

func TestIndex_BranchFilter(t *testing.T) {
	idx := NewIndex()

	idx.Add(makeEvent("ev1", "main", "compaction logic"))
	idx.Add(makeEvent("ev2", "feature-branch", "compaction logic"))

	results := idx.Search("compaction", SearchOpts{BranchID: "main"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result with branch filter, got %d", len(results))
	}
	if results[0].EventID != "ev1" {
		t.Errorf("expected ev1, got %q", results[0].EventID)
	}
}

func TestIndex_Limit(t *testing.T) {
	idx := NewIndex()

	for i := 0; i < 10; i++ {
		idx.Add(makeEvent(
			string(rune('a'+i))+"ev",
			"main",
			"compaction event number",
		))
	}

	results := idx.Search("compaction", SearchOpts{Limit: 3})
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestIndex_EmptyQuery(t *testing.T) {
	idx := NewIndex()
	idx.Add(makeEvent("ev1", "main", "some text"))

	results := idx.Search("", SearchOpts{})
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(results))
	}
}

func TestIndex_ASTBoosting(t *testing.T) {
	idx := NewIndex()

	// One event with Go source containing CompactByScore
	goSrc := `package memory

func CompactByScore(events []Event, scores map[string]float64) []Event {
	// compaction logic here
	return nil
}
`
	idx.Add(makeEvent("ev1", "main", goSrc))
	idx.Add(makeEvent("ev2", "main", "some compaction notes about the system"))

	// Searching for the exact symbol should strongly prefer ev1
	results := idx.Search("CompactByScore", SearchOpts{})
	if len(results) == 0 {
		t.Fatal("expected results for 'CompactByScore'")
	}
	if results[0].EventID != "ev1" {
		t.Errorf("top result = %q, want ev1 (the Go source event)", results[0].EventID)
	}
	t.Logf("symbol search scores: ev1=%.3f (should be much higher)", results[0].Score)
}

func TestIndex_Size(t *testing.T) {
	idx := NewIndex()
	if idx.Size() != 0 {
		t.Errorf("empty index size = %d, want 0", idx.Size())
	}

	idx.Add(makeEvent("ev1", "main", "hello world"))
	idx.Add(makeEvent("ev2", "main", "goodbye world"))
	if idx.Size() != 2 {
		t.Errorf("index size = %d, want 2", idx.Size())
	}
}

// --- eventText tests ---

func TestEventText_PreferText(t *testing.T) {
	ev := eventlog.Event{
		Meta: map[string]any{
			"text":    "primary text content",
			"summary": "fallback summary",
		},
	}
	got := eventText(ev)
	if got != "primary text content" {
		t.Errorf("eventText = %q, want 'primary text content'", got)
	}
}

func TestEventText_FallbackSummary(t *testing.T) {
	ev := eventlog.Event{
		Meta: map[string]any{
			"summary": "fallback summary content",
		},
	}
	got := eventText(ev)
	if got != "fallback summary content" {
		t.Errorf("eventText = %q, want 'fallback summary content'", got)
	}
}

func TestEventText_Empty(t *testing.T) {
	ev := eventlog.Event{}
	got := eventText(ev)
	if got != "" {
		t.Errorf("eventText = %q, want empty", got)
	}
}
