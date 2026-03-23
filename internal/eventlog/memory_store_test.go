package eventlog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreAppendGetAndList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	e1 := Event{
		ID:        "e1",
		SessionID: "s1",
		TS:        ts,
		Kind:      "turn.user",
		BranchID:  "main",
	}
	e2 := Event{
		ID:        "e2",
		SessionID: "s1",
		TS:        ts.Add(time.Second),
		Kind:      "turn.agent",
		BranchID:  "main",
	}

	if err := store.Append(ctx, e1); err != nil {
		t.Fatalf("append e1: %v", err)
	}
	if err := store.Append(ctx, e2); err != nil {
		t.Fatalf("append e2: %v", err)
	}

	got, err := store.GetByID(ctx, "s1", "e1")
	if err != nil {
		t.Fatalf("get e1: %v", err)
	}
	if got.ID != "e1" || got.Kind != "turn.user" {
		t.Fatalf("unexpected event: %#v", got)
	}

	list, err := store.ListBySession(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 events, got %d", len(list))
	}
	if list[0].ID != "e1" || list[1].ID != "e2" {
		t.Fatalf("list order must match append order, got %q -> %q", list[0].ID, list[1].ID)
	}
}

func TestMemoryStoreDuplicateIDRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	e := Event{
		ID:        "dup",
		SessionID: "s1",
		TS:        ts,
		Kind:      "turn.user",
		BranchID:  "main",
	}
	if err := store.Append(ctx, e); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.Append(ctx, e); !errors.Is(err, ErrDuplicateEventID) {
		t.Fatalf("expected ErrDuplicateEventID, got %v", err)
	}
}

func TestMemoryStoreInvalidEventRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()

	err := store.Append(ctx, Event{
		ID:       "missing-session",
		TS:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Kind:     "turn.user",
		BranchID: "main",
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected ErrInvalidEvent, got %v", err)
	}
}
