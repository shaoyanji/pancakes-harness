package dream

import (
	"context"
	"testing"
	"time"

	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/memory"
)

func TestShouldDreamDisabled(t *testing.T) {
	daemon := NewDaemon(Config{Enabled: false}, nil, nil)
	if daemon.ShouldDream() {
		t.Fatal("should not dream when disabled")
	}
}

func TestShouldDreamInactivityNotMet(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 24,
		MinSessions:     5,
	}, memMgr, &mockEventLog{})

	// Record activity now
	daemon.RecordActivity()

	// Should not dream - inactivity not met
	if daemon.ShouldDream() {
		t.Fatal("should not dream immediately after activity")
	}
}

func TestShouldDreamSessionsNotMet(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 0, // no inactivity requirement
		MinSessions:     5,
	}, memMgr, &mockEventLog{})

	// Only 3 sessions
	for i := 0; i < 3; i++ {
		daemon.RecordActivity()
	}

	if daemon.ShouldDream() {
		t.Fatal("should not dream with only 3 sessions")
	}
}

func TestShouldDreamThresholdsMet(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 0,
		MinSessions:     3,
	}, memMgr, &mockEventLog{})

	// Record 5 sessions
	for i := 0; i < 5; i++ {
		daemon.RecordActivity()
	}

	// Simulate inactivity by setting lastActivityTS far in the past
	daemon.mu.Lock()
	daemon.lastActivityTS = time.Now().Add(-48 * time.Hour)
	daemon.mu.Unlock()

	if !daemon.ShouldDream() {
		t.Fatal("should dream when thresholds met")
	}
}

func TestDreamCooldown(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 0,
		MinSessions:     1,
	}, memMgr, &mockEventLog{})

	daemon.RecordActivity()

	// Set lastDream to now
	daemon.mu.Lock()
	daemon.lastDreamTime = time.Now()
	daemon.mu.Unlock()

	// Should not dream due to cooldown
	if daemon.ShouldDream() {
		t.Fatal("should not dream during cooldown")
	}
}

func TestExecuteEmptyEvents(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 0,
		MinSessions:     1,
	}, memMgr, &mockEventLog{events: []eventlog.Event{}})

	result, err := daemon.Execute(context.Background(), "test-session")
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsReviewed != 0 {
		t.Fatalf("expected 0 events reviewed, got %d", result.EventsReviewed)
	}
}

func TestDreamCount(t *testing.T) {
	memMgr := memory.NewManager(memory.Config{})
	daemon := NewDaemon(Config{
		Enabled:         true,
		InactivityHours: 0,
		MinSessions:     1,
	}, memMgr, &mockEventLog{events: []eventlog.Event{}})

	// Execute a dream pass
	_, _ = daemon.Execute(context.Background(), "s1")

	count := daemon.DreamCount()
	if count != 1 {
		t.Fatalf("expected dream count 1, got %d", count)
	}
}

func TestIdentifyTopics(t *testing.T) {
	daemon := NewDaemon(Config{}, nil, nil)

	events := []eventlog.Event{
		{ID: "1", Kind: "turn.user", TS: time.Now(), Meta: map[string]any{"task_summary": "hello world"}},
		{ID: "2", Kind: "turn.user", TS: time.Now(), Meta: map[string]any{"task_summary": "hello again"}},
		{ID: "3", Kind: "turn.agent", TS: time.Now()},
		{ID: "4", Kind: "turn.agent", TS: time.Now()},
	}

	topics := daemon.identifyTopics(events)
	if len(topics) == 0 {
		t.Fatal("expected some topics")
	}
	// "turn" topic should have 4 events (2 user + 2 agent)
	turnEvents, ok := topics["turn"]
	if !ok {
		t.Fatal("expected 'turn' topic")
	}
	if len(turnEvents) != 4 {
		t.Fatalf("expected 4 turn events, got %d", len(turnEvents))
	}
}

func TestSynthesizeSummary(t *testing.T) {
	daemon := NewDaemon(Config{}, nil, nil)

	events := []eventlog.Event{
		{ID: "1", Kind: "turn.user", TS: time.Now()},
		{ID: "2", Kind: "turn.user", TS: time.Now().Add(time.Minute)},
		{ID: "3", Kind: "turn.agent", TS: time.Now().Add(2 * time.Minute)},
	}

	summary := daemon.synthesizeSummary(events)
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

type mockEventLog struct {
	events []eventlog.Event
}

func (m *mockEventLog) ListBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error) {
	return m.events, nil
}

func (m *mockEventLog) AppendEvent(ctx context.Context, e eventlog.Event) error {
	return nil
}
