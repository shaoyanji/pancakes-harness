package backend

import (
	"context"
	"testing"

	"pancakes-harness/internal/consult"
)

// BackendComplianceTest is a reusable test suite for backend implementations.
// Every adapter must pass this suite to be considered compliant.
func BackendComplianceTest(t *testing.T, factory func(testing.TB) Backend) {
	t.Helper()

	t.Run("SaveLoadManifestRoundTrip", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		m := consult.Manifest{
			EventID:           "test-manifest-1",
			SessionID:         "test-session",
			BranchID:          "main",
			Fingerprint:       "fp-test-1",
			Mode:              "agent_call",
			ByteBudget:        14336,
			ActualBytes:       640,
			SerializerVersion: consult.SerializerVersionV1,
			TaskSummary:       "test task",
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
		if loaded.Fingerprint != m.Fingerprint {
			t.Errorf("Fingerprint mismatch: got %q, want %q", loaded.Fingerprint, m.Fingerprint)
		}
		if loaded.SerializerVersion != m.SerializerVersion {
			t.Errorf("SerializerVersion mismatch: got %q, want %q", loaded.SerializerVersion, m.SerializerVersion)
		}
	})

	t.Run("SaveLoadEventRoundTrip", func(t *testing.T) {
		b := factory(t)
		ctx := context.Background()

		e := consult.EventSummary{
			SchemaVersion:             consult.EventSchemaVersionV1,
			Fingerprint:               "test-event-1",
			ManifestSerializerVersion: consult.SerializerVersionV1,
			Outcome:                   consult.OutcomeResolved,
			Role:                      consult.RoleLeader,
			ByteBudget:                14336,
			ActualBytes:               640,
			TaskSummary:                "test task",
		}

		if err := b.SaveEvent(ctx, e); err != nil {
			t.Fatalf("SaveEvent failed: %v", err)
		}

		loaded, err := b.LoadEvent(ctx, "test-event-1")
		if err != nil {
			t.Fatalf("LoadEvent failed: %v", err)
		}

		if loaded.Fingerprint != e.Fingerprint {
			t.Errorf("Fingerprint mismatch: got %q, want %q", loaded.Fingerprint, e.Fingerprint)
		}
		if loaded.SchemaVersion != e.SchemaVersion {
			t.Errorf("SchemaVersion mismatch: got %q, want %q", loaded.SchemaVersion, e.SchemaVersion)
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
			m := consult.Manifest{
				EventID:           id,
				SessionID:         "test-session",
				BranchID:          "main",
				Fingerprint:       "fp-" + id,
				Mode:              "agent_call",
				ByteBudget:        14336,
				SerializerVersion: consult.SerializerVersionV1,
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
			id := "stream-test-" + string(rune('0'+i))
			m := consult.Manifest{
				EventID:           id,
				SessionID:         "test-session",
				BranchID:          "main",
				Fingerprint:       "fp-" + id,
				Mode:              "agent_call",
				ByteBudget:        14336,
				SerializerVersion: consult.SerializerVersionV1,
			}
			expected[id] = true
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
