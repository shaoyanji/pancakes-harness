package consult

import (
	"encoding/json"
	"testing"
)

func TestSerialiser_ManifestRoundTrip(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	m := ManifestV1{
		Version:      Version,
		EventID:      "evt-123",
		Timestamp:    1700000000000000000,
		Model:        "gpt-4",
		Cost:         0.05,
		Status:       "completed",
		ParentEvents: []string{"evt-100", "evt-101"},
	}

	data, err := s.EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest failed: %v", err)
	}

	decoded, err := s.DecodeManifest(data)
	if err != nil {
		t.Fatalf("DecodeManifest failed: %v", err)
	}

	if decoded.Version != m.Version {
		t.Errorf("version mismatch: got %d, want %d", decoded.Version, m.Version)
	}
	if decoded.EventID != m.EventID {
		t.Errorf("eventID mismatch: got %q, want %q", decoded.EventID, m.EventID)
	}
	if decoded.Timestamp != m.Timestamp {
		t.Errorf("timestamp mismatch: got %d, want %d", decoded.Timestamp, m.Timestamp)
	}
	if decoded.Model != m.Model {
		t.Errorf("model mismatch: got %q, want %q", decoded.Model, m.Model)
	}
	if decoded.Cost != m.Cost {
		t.Errorf("cost mismatch: got %f, want %f", decoded.Cost, m.Cost)
	}
	if decoded.Status != m.Status {
		t.Errorf("status mismatch: got %q, want %q", decoded.Status, m.Status)
	}
	if len(decoded.ParentEvents) != len(m.ParentEvents) {
		t.Fatalf("parentEvents length mismatch: got %d, want %d", len(decoded.ParentEvents), len(m.ParentEvents))
	}
	for i, pe := range decoded.ParentEvents {
		if pe != m.ParentEvents[i] {
			t.Errorf("parentEvents[%d] mismatch: got %q, want %q", i, pe, m.ParentEvents[i])
		}
	}
}

func TestSerialiser_EventRoundTrip(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	manifest := ManifestV1{
		Version:   Version,
		EventID:   "evt-456",
		Timestamp: 1700000000000000000,
		Model:     "gpt-4",
		Cost:      0.10,
		Status:    "completed",
	}

	e := EventV1{
		Version:  Version,
		EventID:  "evt-456",
		Manifest: manifest,
		Request:  json.RawMessage(`{"query":"test"}`),
		Response: json.RawMessage(`{"answer":"response"}`),
		Meta:     json.RawMessage(`{"source":"test"}`),
	}

	data, err := s.EncodeEvent(e)
	if err != nil {
		t.Fatalf("EncodeEvent failed: %v", err)
	}

	decoded, err := s.DecodeEvent(data)
	if err != nil {
		t.Fatalf("DecodeEvent failed: %v", err)
	}

	if decoded.Version != e.Version {
		t.Errorf("event version mismatch: got %d, want %d", decoded.Version, e.Version)
	}
	if decoded.EventID != e.EventID {
		t.Errorf("event eventID mismatch: got %q, want %q", decoded.EventID, e.EventID)
	}
	if decoded.Manifest.Version != e.Manifest.Version {
		t.Errorf("manifest version mismatch: got %d, want %d", decoded.Manifest.Version, e.Manifest.Version)
	}
	if string(decoded.Request) != string(e.Request) {
		t.Errorf("request mismatch: got %q, want %q", string(decoded.Request), string(e.Request))
	}
	if string(decoded.Response) != string(e.Response) {
		t.Errorf("response mismatch: got %q, want %q", string(decoded.Response), string(e.Response))
	}
	if string(decoded.Meta) != string(e.Meta) {
		t.Errorf("meta mismatch: got %q, want %q", string(decoded.Meta), string(e.Meta))
	}
}

func TestSerialiser_DecodeUnsupportedManifestVersion(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	// Craft a manifest with unsupported version
	badData := []byte(`{"version":99,"event_id":"evt-bad","timestamp":0,"model":"x","cost":0,"status":"ok"}`)

	_, err := s.DecodeManifest(badData)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !contains(err.Error(), "unsupported manifest version") {
		t.Errorf("expected version error, got: %v", err)
	}
}

func TestSerialiser_DecodeUnsupportedEventVersion(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	// Craft an event with unsupported version
	badData := []byte(`{"version":99,"event_id":"evt-bad","manifest":{"version":99,"event_id":"evt-bad","timestamp":0,"model":"x","cost":0,"status":"ok"},"request":{},"response":{}}`)

	_, err := s.DecodeEvent(badData)
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !contains(err.Error(), "version mismatch") {
		t.Errorf("expected version mismatch error, got: %v", err)
	}
}

func TestSerialiser_ManifestInsideEventMatchesStandalone(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	manifest := ManifestV1{
		Version:   Version,
		EventID:   "evt-789",
		Timestamp: 1700000000000000000,
		Model:     "claude-3",
		Cost:      0.02,
		Status:    "recovery",
	}

	// Encode standalone manifest
	standaloneData, err := s.EncodeManifest(manifest)
	if err != nil {
		t.Fatalf("EncodeManifest failed: %v", err)
	}

	// Create event with same manifest
	event := EventV1{
		Version:  Version,
		EventID:  "evt-789",
		Manifest: manifest,
		Request:  json.RawMessage(`{}`),
		Response: json.RawMessage(`{}`),
	}

	eventData, err := s.EncodeEvent(event)
	if err != nil {
		t.Fatalf("EncodeEvent failed: %v", err)
	}

	// Decode event and extract manifest
	decodedEvent, err := s.DecodeEvent(eventData)
	if err != nil {
		t.Fatalf("DecodeEvent failed: %v", err)
	}

	// Encode the extracted manifest
	extractedData, err := s.EncodeManifest(decodedEvent.Manifest)
	if err != nil {
		t.Fatalf("EncodeManifest (extracted) failed: %v", err)
	}

	// Compare byte-for-byte
	if string(standaloneData) != string(extractedData) {
		t.Errorf("standalone manifest and event-embedded manifest differ:\nstandalone: %s\nextracted: %s",
			string(standaloneData), string(extractedData))
	}
}

func TestSerialiser_DecodeMalformedJSON(t *testing.T) {
	t.Parallel()

	s := Serialiser{}
	badData := []byte(`{invalid json}`)

	_, err := s.DecodeManifest(badData)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !contains(err.Error(), "manifest decode") {
		t.Errorf("expected decode error, got: %v", err)
	}

	_, err = s.DecodeEvent(badData)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !contains(err.Error(), "event decode") {
		t.Errorf("expected decode error, got: %v", err)
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
