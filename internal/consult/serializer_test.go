package consult

import (
	"testing"
)

func TestEncodeDecodeManifest(t *testing.T) {
	t.Parallel()

	m := Manifest{
		EventID:           "evt-123",
		SessionID:         "sess-1",
		BranchID:          "main",
		Fingerprint:       "fp-123",
		Mode:              "agent_call",
		ByteBudget:        14336,
		ActualBytes:       640,
		SerializerVersion: SerializerVersionV1,
		TaskSummary:       "test task",
	}

	data, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest failed: %v", err)
	}

	decoded, err := DecodeManifest(data)
	if err != nil {
		t.Fatalf("DecodeManifest failed: %v", err)
	}

	if decoded.EventID != m.EventID {
		t.Errorf("EventID mismatch: got %q, want %q", decoded.EventID, m.EventID)
	}
	if decoded.SessionID != m.SessionID {
		t.Errorf("SessionID mismatch: got %q, want %q", decoded.SessionID, m.SessionID)
	}
	if decoded.Fingerprint != m.Fingerprint {
		t.Errorf("Fingerprint mismatch: got %q, want %q", decoded.Fingerprint, m.Fingerprint)
	}
	if decoded.SerializerVersion != m.SerializerVersion {
		t.Errorf("SerializerVersion mismatch: got %q, want %q", decoded.SerializerVersion, m.SerializerVersion)
	}
	if decoded.ByteBudget != m.ByteBudget {
		t.Errorf("ByteBudget mismatch: got %d, want %d", decoded.ByteBudget, m.ByteBudget)
	}
}

func TestEncodeDecodeEventSummary(t *testing.T) {
	t.Parallel()

	e := EventSummary{
		SchemaVersion:             EventSchemaVersionV1,
		Fingerprint:               "fp-456",
		ManifestSerializerVersion: SerializerVersionV1,
		Outcome:                   OutcomeResolved,
		Role:                      RoleLeader,
		ByteBudget:                14336,
		ActualBytes:               640,
		TaskSummary:                "test task",
	}

	data, err := EncodeEventSummary(e)
	if err != nil {
		t.Fatalf("EncodeEventSummary failed: %v", err)
	}

	decoded, err := DecodeEventSummary(data)
	if err != nil {
		t.Fatalf("DecodeEventSummary failed: %v", err)
	}

	if decoded.Fingerprint != e.Fingerprint {
		t.Errorf("Fingerprint mismatch: got %q, want %q", decoded.Fingerprint, e.Fingerprint)
	}
	if decoded.SchemaVersion != e.SchemaVersion {
		t.Errorf("SchemaVersion mismatch: got %q, want %q", decoded.SchemaVersion, e.SchemaVersion)
	}
	if decoded.ManifestSerializerVersion != e.ManifestSerializerVersion {
		t.Errorf("ManifestSerializerVersion mismatch: got %q, want %q", decoded.ManifestSerializerVersion, e.ManifestSerializerVersion)
	}
	if decoded.Outcome != e.Outcome {
		t.Errorf("Outcome mismatch: got %q, want %q", decoded.Outcome, e.Outcome)
	}
}

func TestDecodeUnsupportedManifestVersion(t *testing.T) {
	t.Parallel()

	// Craft a manifest with unsupported version
	badData := []byte(`{"event_id":"evt-bad","serializer_version":"unsupported.v1"}`)

	_, err := DecodeManifest(badData)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !contains(err.Error(), "unsupported manifest serializer version") {
		t.Errorf("expected version error, got: %v", err)
	}
}

func TestDecodeUnsupportedEventVersion(t *testing.T) {
	t.Parallel()

	// Craft an event with unsupported schema version
	badData := []byte(`{"schema_version":"unsupported.v1","fingerprint":"evt-bad"}`)

	_, err := DecodeEventSummary(badData)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !contains(err.Error(), "unsupported event schema version") {
		t.Errorf("expected version error, got: %v", err)
	}
}

func TestManifestEventVersionAlignment(t *testing.T) {
	t.Parallel()

	// Event with mismatched manifest serializer version should fail
	e := EventSummary{
		SchemaVersion:             EventSchemaVersionV1,
		Fingerprint:               "fp-align-test",
		ManifestSerializerVersion: "mismatched.v1",
		Outcome:                   OutcomeResolved,
	}

	_, err := EncodeEventSummary(e)
	if err == nil {
		t.Fatal("expected error for mismatched manifest version, got nil")
	}
	if !contains(err.Error(), "does not align") {
		t.Errorf("expected alignment error, got: %v", err)
	}
}

func TestDecodeMalformedJSON(t *testing.T) {
	t.Parallel()

	badData := []byte(`{invalid json}`)

	_, err := DecodeManifest(badData)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !contains(err.Error(), "manifest decode") {
		t.Errorf("expected decode error, got: %v", err)
	}

	_, err = DecodeEventSummary(badData)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !contains(err.Error(), "event summary decode") {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestVersionInfo(t *testing.T) {
	t.Parallel()

	info := VersionInfo()
	if info["serializer_version"] != SerializerVersionV1 {
		t.Errorf("serializer_version mismatch: got %q, want %q", info["serializer_version"], SerializerVersionV1)
	}
	if info["event_schema_version"] != EventSchemaVersionV1 {
		t.Errorf("event_schema_version mismatch: got %q, want %q", info["event_schema_version"], EventSchemaVersionV1)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
