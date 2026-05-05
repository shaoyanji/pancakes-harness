package backend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pancakes-harness/internal/consult"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/replay"
)

// BackendComplianceTest is a reusable test suite for backend implementations.
// Every adapter must pass this suite to be considered compliant.
func BackendComplianceTest(t *testing.T, factory func(testing.TB) Backend) {
	t.Helper()

	t.Run("SaveLoadManifestRoundTrip", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		m := consult.ManifestV1{
			Version:      consult.Version,
			EventID:      "test-manifest-1",
			Timestamp:    time.Now().UnixNano(),
			Model:        "gpt-4",
			Cost:         0.05,
			Status:       "completed",
			ParentEvents: []string{"parent-1", "parent-2"},
		}

		if err := b.SaveManifest(ctx, m); err != nil {
			t.Fatalf("SaveManifest failed: %v", err)
		}

		loaded, err := b.LoadManifest(ctx, "test-manifest-1")
		if err != nil {
			t.Fatalf("LoadManifest failed: %v", err)
		}

		if loaded.EventID != m.EventID {
			t.Errorf("EventID mismatch: got %q, want %q", loaded.EventID, m.EventID)
		}
		if loaded.Model != m.Model {
			t.Errorf("Model mismatch: got %q, want %q", loaded.Model, m.Model)
		}
		if loaded.Status != m.Status {
			t.Errorf("Status mismatch: got %q, want %q", loaded.Status, m.Status)
		}
	})

	t.Run("SaveLoadEventRoundTrip", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		manifest := consult.ManifestV1{
			Version:   consult.Version,
			EventID:   "test-event-1",
			Timestamp: time.Now().UnixNano(),
			Model:     "claude-3",
			Cost:      0.10,
			Status:    "completed",
		}

		e := consult.EventV1{
			Version:  consult.Version,
			EventID:  "test-event-1",
			Manifest: manifest,
			Request:  json.RawMessage(`{"query":"test"}`),
			Response: json.RawMessage(`{"answer":"response"}`),
			Meta:     json.RawMessage(`{"source":"test"}`),
		}

		if err := b.SaveEvent(ctx, e); err != nil {
			t.Fatalf("SaveEvent failed: %v", err)
		}

		loaded, err := b.LoadEvent(ctx, "test-event-1")
		if err != nil {
			t.Fatalf("LoadEvent failed: %v", err)
		}

		if loaded.EventID != e.EventID {
			t.Errorf("EventID mismatch: got %q, want %q", loaded.EventID, e.EventID)
		}
		if string(loaded.Request) != string(e.Request) {
			t.Errorf("Request mismatch: got %q, want %q", string(loaded.Request), string(e.Request))
		}
	})

	t.Run("ManifestNotFound", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		_, err := b.LoadManifest(ctx, "nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent manifest, got nil")
		}
		if err != ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("EventNotFound", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		_, err := b.LoadEvent(ctx, "nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent event, got nil")
		}
		if err != ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("ListManifestsPagination", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		// Create 10 manifests with ordered IDs
		expectedOrder := []string{}
		for i := 0; i < 10; i++ {
			id := "list-test-" + string(rune('0'+i))
			m := consult.ManifestV1{
				Version:   consult.Version,
				EventID:   id,
				Timestamp: time.Now().UnixNano(),
				Model:     "gpt-4",
				Status:    "completed",
			}
			expectedOrder = append(expectedOrder, id)
			if err := b.SaveManifest(ctx, m); err != nil {
				t.Fatalf("SaveManifest failed: %v", err)
			}
		}

		// Get first 5
		first, err := b.ListManifests(ctx, 5, 0)
		if err != nil {
			t.Fatalf("ListManifests failed: %v", err)
		}
		if len(first) != 5 {
			t.Errorf("expected 5 manifests, got %d", len(first))
		}

		// Get next 5 with offset
		second, err := b.ListManifests(ctx, 5, 5)
		if err != nil {
			t.Fatalf("ListManifests with offset failed: %v", err)
		}
		if len(second) != 5 {
			t.Errorf("expected 5 manifests with offset, got %d", len(second))
		}

		// Ensure we got all 10 unique manifests
		allIDs := make(map[string]bool)
		for _, m := range first {
			allIDs[m.EventID] = true
		}
		for _, m := range second {
			if allIDs[m.EventID] {
				t.Errorf("overlap detected: %s in both pages", m.EventID)
			}
			allIDs[m.EventID] = true
		}
		if len(allIDs) != 10 {
			t.Errorf("expected 10 unique manifests across pages, got %d", len(allIDs))
		}
	})

	t.Run("StreamManifests", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		// Create 5 manifests
		expected := make(map[string]bool)
		for i := 0; i < 5; i++ {
			m := consult.ManifestV1{
				Version:   consult.Version,
				EventID:   "stream-test-" + string(rune('0'+i)),
				Timestamp: time.Now().UnixNano(),
				Model:     "gpt-4",
				Status:    "completed",
			}
			expected[m.EventID] = true
			if err := b.SaveManifest(ctx, m); err != nil {
				t.Fatalf("SaveManifest failed: %v", err)
			}
		}

		ch, errCh := b.StreamManifests(ctx)
		received := make(map[string]bool)
		for m := range ch {
			received[m.EventID] = true
		}
		if err := <-errCh; err != nil {
			t.Fatalf("StreamManifests returned error: %v", err)
		}

		if len(received) != 5 {
			t.Errorf("expected 5 manifests from stream, got %d", len(received))
		}

		// Verify all expected manifests were received
		for id := range expected {
			if !received[id] {
				t.Errorf("missing manifest %s from stream", id)
			}
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		b := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := b.LoadManifest(ctx, "test")
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}

		_, err = b.LoadEvent(ctx, "test")
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}

		_, err = b.ListManifests(ctx, 10, 0)
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("PingAndClose", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		if err := b.Ping(ctx); err != nil {
			t.Errorf("Ping failed: %v", err)
		}

		if err := b.Close(); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})

	t.Run("HealthCheck", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		status := b.HealthCheck(ctx)
		// Health check should not panic and should return a valid status
		if !status.OK && len(status.Diagnostics) == 0 {
			t.Error("unhealthy status should have diagnostics")
		}
	})
}

func TestBackendAdapterCanBeSwappedWithoutRuntimeChanges(t *testing.T) {
	t.Parallel()

	mem := NewMemoryBackend()

	runScenario := func(t *testing.T, name string, b Backend) {
		t.Helper()
		ctx := context.Background()
		ts := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)

		events := []eventlog.Event{
			{ID: "e1", SessionID: "s1", TS: ts, Kind: eventlog.KindTurnUser, BranchID: "main"},
			{ID: "e2", SessionID: "s1", TS: ts.Add(time.Second), Kind: eventlog.KindToolRequest, BranchID: "main"},
			{ID: "e3", SessionID: "s1", TS: ts.Add(2 * time.Second), Kind: eventlog.KindToolResult, BranchID: "main", BlobRef: "blob://tool/1"},
		}
		for _, e := range events {
			if err := b.AppendEvent(ctx, e); err != nil {
				t.Fatalf("%s append event: %v", name, err)
			}
		}
		if err := b.AppendBlob(ctx, "blob://tool/1", []byte("payload")); err != nil {
			t.Fatalf("%s append blob: %v", name, err)
		}

		sessionEvents, err := b.ListEventsBySession(ctx, "s1")
		if err != nil {
			t.Fatalf("%s list session: %v", name, err)
		}
		state, err := replay.RebuildSession(sessionEvents)
		if err != nil {
			t.Fatalf("%s rebuild session: %v", name, err)
		}
		if state.BranchHeads["main"] != "e3" {
			t.Fatalf("%s expected head e3, got %q", name, state.BranchHeads["main"])
		}

		branchEvents, err := b.ListEventsByBranch(ctx, "s1", "main")
		if err != nil {
			t.Fatalf("%s list branch: %v", name, err)
		}
		if len(branchEvents) != 3 {
			t.Fatalf("%s expected 3 branch events, got %d", name, len(branchEvents))
		}

		blob, err := b.FetchBlob(ctx, "blob://tool/1")
		if err != nil {
			t.Fatalf("%s fetch blob: %v", name, err)
		}
		if string(blob) != "payload" {
			t.Fatalf("%s expected payload blob, got %q", name, string(blob))
		}
	}

	runScenario(t, "memory", mem)
}
