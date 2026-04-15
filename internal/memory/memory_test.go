package memory

import (
	"fmt"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
)

func TestIndexEvent(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 10})

	ev := eventlog.Event{
		ID:        "test.001",
		SessionID: "session1",
		BranchID:  "main",
		Kind:      "turn.user",
		TS:        time.Now(),
		Meta: map[string]any{
			"text":        "hello world",
			"fingerprint": "abc123",
		},
	}
	mgr.IndexEvent(ev)

	entries := mgr.LookupRecent(10, "")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].EventID != "test.001" {
		t.Fatalf("expected event ID test.001, got %s", entries[0].EventID)
	}
	if entries[0].Fingerprint != "abc123" {
		t.Fatalf("expected fingerprint abc123, got %s", entries[0].Fingerprint)
	}
}

func TestLookupByFingerprint(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 10})

	for i := 0; i < 5; i++ {
		fp := "fp_a"
		if i >= 3 {
			fp = "fp_b"
		}
		mgr.IndexEvent(eventlog.Event{
			ID:        fmt.Sprintf("test.%03d", i),
			SessionID: "session1",
			BranchID:  "main",
			Kind:      "turn.user",
			TS:        time.Now().Add(time.Duration(i) * time.Minute),
			Meta:      map[string]any{"fingerprint": fp},
		})
	}

	results := mgr.LookupByFingerprint("fp_a")
	if len(results) != 3 {
		t.Fatalf("expected 3 results for fp_a, got %d", len(results))
	}

	resultsB := mgr.LookupByFingerprint("fp_b")
	if len(resultsB) != 2 {
		t.Fatalf("expected 2 results for fp_b, got %d", len(resultsB))
	}
}

func TestLookupByKind(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 10})

	mgr.IndexEvent(eventlog.Event{ID: "1", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now()})
	mgr.IndexEvent(eventlog.Event{ID: "2", SessionID: "s", BranchID: "m", Kind: "turn.agent", TS: time.Now()})
	mgr.IndexEvent(eventlog.Event{ID: "3", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now()})

	results := mgr.LookupRecent(10, "turn.user")
	if len(results) != 2 {
		t.Fatalf("expected 2 turn.user events, got %d", len(results))
	}
}

func TestCacheHitRate(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 10})

	mgr.IndexEvent(eventlog.Event{
		ID: "1", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now(),
		Meta: map[string]any{"fingerprint": "fp1"},
	})

	// Cache hit
	mgr.LookupByFingerprint("fp1")
	// Cache miss
	mgr.LookupByFingerprint("fp_nonexistent")

	rate := mgr.CacheHitRate()
	if rate != 0.5 {
		t.Fatalf("expected 0.5 hit rate, got %f", rate)
	}
}

func TestTopicCreateAndGet(t *testing.T) {
	mgr := NewManager(Config{})

	topic := TopicMemory{
		TopicID:   "test_topic",
		Title:     "Test Topic",
		Summary:   "This is a test summary",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := mgr.CreateTopic(topic); err != nil {
		t.Fatalf("create topic: %v", err)
	}

	got, ok := mgr.GetTopic("test_topic")
	if !ok {
		t.Fatal("expected topic to exist")
	}
	if got.Title != "Test Topic" {
		t.Fatalf("expected title 'Test Topic', got %q", got.Title)
	}
}

func TestTopicUpdate(t *testing.T) {
	mgr := NewManager(Config{})

	topic := TopicMemory{TopicID: "t1", Title: "T1", Summary: "initial", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := mgr.CreateTopic(topic); err != nil {
		t.Fatal(err)
	}

	if err := mgr.UpdateTopic("t1", "updated summary", []string{"ev1", "ev2"}, []string{"tag1"}); err != nil {
		t.Fatal(err)
	}

	got, _ := mgr.GetTopic("t1")
	if got.Summary != "updated summary" {
		t.Fatalf("expected updated summary, got %q", got.Summary)
	}
	if len(got.SourceEvents) != 2 {
		t.Fatalf("expected 2 source events, got %d", len(got.SourceEvents))
	}
}

func TestListTopics(t *testing.T) {
	mgr := NewManager(Config{})

	mgr.CreateTopic(TopicMemory{TopicID: "a", Title: "A", Summary: "s1", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	mgr.CreateTopic(TopicMemory{TopicID: "b", Title: "B", Summary: "s2", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	topics := mgr.ListTopics()
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}
}

func TestDeleteTopic(t *testing.T) {
	mgr := NewManager(Config{})

	topic := TopicMemory{TopicID: "del", Title: "Del", Summary: "s", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	mgr.CreateTopic(topic)
	mgr.DeleteTopic("del")

	_, ok := mgr.GetTopic("del")
	if ok {
		t.Fatal("expected topic to be deleted")
	}
}

func TestBuildCompactView(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 100})

	events := make([]eventlog.Event, 10)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("ev.%03d", i)
		mgr.IndexEvent(eventlog.Event{
			ID: id, SessionID: "s", BranchID: "m", Kind: "turn.user",
			TS: time.Now().Add(time.Duration(i) * time.Minute),
			Meta: map[string]any{"text": "some text content here"},
		})
		events[i] = eventlog.Event{
			ID: id, SessionID: "s", BranchID: "m", Kind: "turn.user",
			TS: time.Now().Add(time.Duration(i) * time.Minute),
		}
	}

	selected, kept, removed := mgr.BuildCompactView(events, 500, "")
	if kept+removed != 10 {
		t.Fatalf("expected kept+removed=10, got %d+%d", kept, removed)
	}
	if len(selected) != kept {
		t.Fatalf("expected selected=%d, got %d", kept, len(selected))
	}
}

func TestFork(t *testing.T) {
	mgr := NewManager(Config{})

	events := []eventlog.Event{
		{ID: "e1", SessionID: "s", BranchID: "m", Kind: "turn.user", TS: time.Now()},
		{ID: "e2", SessionID: "s", BranchID: "m", Kind: "turn.agent", TS: time.Now()},
	}

	if err := mgr.Fork("fork1", "Fork Topic", events, "summary of fork1"); err != nil {
		t.Fatal(err)
	}

	got, ok := mgr.GetTopic("fork1")
	if !ok {
		t.Fatal("expected fork topic to exist")
	}
	if got.Title != "Fork Topic" {
		t.Fatalf("expected title 'Fork Topic', got %q", got.Title)
	}
	if len(got.SourceEvents) != 2 {
		t.Fatalf("expected 2 source events, got %d", len(got.SourceEvents))
	}
}

func TestIndexTrimming(t *testing.T) {
	mgr := NewManager(Config{MaxIndexEntries: 5})

	for i := 0; i < 10; i++ {
		mgr.IndexEvent(eventlog.Event{
			ID: fmt.Sprintf("ev.%03d", i), SessionID: "s", BranchID: "m",
			Kind: "turn.user", TS: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}

	stats := mgr.IndexStats()
	if stats.TotalEntries > 5 {
		t.Fatalf("expected max 5 entries, got %d", stats.TotalEntries)
	}
}
