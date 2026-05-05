package review

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pancakes-harness/internal/consult"
)

func TestReviewSvc_ListRecent(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	// Add test events
	events := []consult.EventV1{
		CreateTestEvent("evt-1", "gpt-4", "completed"),
		CreateTestEvent("evt-2", "claude-3", "completed"),
		CreateTestEvent("evt-3", "gpt-4", "recovery"),
	}
	if err := store.AddTestEvents(events); err != nil {
		t.Fatalf("AddTestEvents failed: %v", err)
	}

	ctx := context.Background()
	manifests, err := svc.ListRecent(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}

	if len(manifests) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(manifests))
	}
}

func TestReviewSvc_Show(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	event := CreateTestEvent("evt-show", "gpt-4", "completed")
	if err := store.AddTestEvents([]consult.EventV1{event}); err != nil {
		t.Fatalf("AddTestEvents failed: %v", err)
	}

	ctx := context.Background()
	result, err := svc.Show(ctx, "evt-show")
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	if result.EventID != "evt-show" {
		t.Errorf("expected event ID evt-show, got %q", result.EventID)
	}
	if result.Manifest.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %q", result.Manifest.Model)
	}
}

func TestReviewSvc_ShowNotFound(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	ctx := context.Background()
	_, err := svc.Show(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestReviewSvc_Export(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	events := []consult.EventV1{
		CreateTestEvent("evt-export-1", "gpt-4", "completed"),
		CreateTestEvent("evt-export-2", "claude-3", "recovery"),
	}
	if err := store.AddTestEvents(events); err != nil {
		t.Fatalf("AddTestEvents failed: %v", err)
	}

	ctx := context.Background()
	var buf bytes.Buffer
	err := svc.Export(ctx, &buf)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines in export, got %d", len(lines))
	}

	// Verify each line is valid JSON
	for i, line := range lines {
		var e consult.EventV1
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestReviewSvc_ExportEmpty(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	ctx := context.Background()
	var buf bytes.Buffer
	err := svc.Export(ctx, &buf)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	output := strings.TrimSpace(buf.String())
	if output != "" {
		t.Errorf("expected empty output for empty store, got %q", output)
	}
}

func TestReviewSvc_ListRecentPagination(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := NewReviewSvc(store)

	// Add 10 events
	for i := 0; i < 10; i++ {
		event := CreateTestEvent("evt-page-"+string(rune('0'+i)), "gpt-4", "completed")
		if err := store.AddTestEvents([]consult.EventV1{event}); err != nil {
			t.Fatalf("AddTestEvents failed: %v", err)
		}
	}

	ctx := context.Background()

	// Get first 5
	manifests, err := svc.ListRecent(ctx, 5)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(manifests) != 5 {
		t.Errorf("expected 5 manifests, got %d", len(manifests))
	}

	// Get next 5 with offset
	manifests, err = store.ListManifests(ctx, 5, 5)
	if err != nil {
		t.Fatalf("ListManifests failed: %v", err)
	}
	if len(manifests) != 5 {
		t.Errorf("expected 5 manifests with offset, got %d", len(manifests))
	}
}

func TestMemoryStore_Count(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	if store.Count() != 0 {
		t.Errorf("expected count 0, got %d", store.Count())
	}

	events := []consult.EventV1{
		CreateTestEvent("evt-1", "gpt-4", "completed"),
		CreateTestEvent("evt-2", "claude-3", "completed"),
	}
	if err := store.AddTestEvents(events); err != nil {
		t.Fatalf("AddTestEvents failed: %v", err)
	}

	if store.Count() != 2 {
		t.Errorf("expected count 2, got %d", store.Count())
	}
}

func TestMemoryStore_StreamManifests(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	events := []consult.EventV1{
		CreateTestEvent("evt-stream-1", "gpt-4", "completed"),
		CreateTestEvent("evt-stream-2", "claude-3", "recovery"),
	}
	if err := store.AddTestEvents(events); err != nil {
		t.Fatalf("AddTestEvents failed: %v", err)
	}

	ctx := context.Background()
	ch, errCh := store.StreamManifests(ctx)

	var received []consult.ManifestV1
	for m := range ch {
		received = append(received, m)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("StreamManifests returned error: %v", err)
	}

	if len(received) != 2 {
		t.Errorf("expected 2 manifests from stream, got %d", len(received))
	}
}
