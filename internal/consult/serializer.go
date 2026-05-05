package consult

import (
	"encoding/json"
	"fmt"
	"time"
)

// Version is the current serialisation format version.
const Version = 1

// ManifestV1 is the consult index record.
type ManifestV1 struct {
	Version      int      `json:"version"`
	EventID      string   `json:"event_id"`
	Timestamp    int64    `json:"timestamp"` // UNIX nanos
	Model        string   `json:"model"`
	Cost         float64  `json:"cost"`
	Status       string   `json:"status"` // "completed", "error", "recovery"
	ParentEvents []string `json:"parent_events,omitempty"`
}

// EventV1 is the full consult receipt.
type EventV1 struct {
	Version  int            `json:"version"`
	EventID  string         `json:"event_id"`
	Manifest ManifestV1     `json:"manifest"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
	Meta     json.RawMessage `json:"meta,omitempty"`
}

// Serialiser handles versioned encode/decode.
type Serialiser struct{}

// EncodeManifest encodes a manifest with version enforcement.
func (s Serialiser) EncodeManifest(m ManifestV1) ([]byte, error) {
	m.Version = Version
	return json.Marshal(m)
}

// DecodeManifest decodes a manifest and enforces version compatibility.
func (s Serialiser) DecodeManifest(data []byte) (ManifestV1, error) {
	var m ManifestV1
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest decode: %w", err)
	}
	if m.Version != Version {
		return m, fmt.Errorf("unsupported manifest version %d, expected %d", m.Version, Version)
	}
	return m, nil
}

// EncodeEvent encodes an event with version enforcement for both event and embedded manifest.
func (s Serialiser) EncodeEvent(e EventV1) ([]byte, error) {
	e.Version = Version
	e.Manifest.Version = Version
	return json.Marshal(e)
}

// DecodeEvent decodes an event and enforces version compatibility for both event and manifest.
func (s Serialiser) DecodeEvent(data []byte) (EventV1, error) {
	var e EventV1
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("event decode: %w", err)
	}
	if e.Version != Version || e.Manifest.Version != Version {
		return e, fmt.Errorf("version mismatch: event %d / manifest %d, expected %d",
			e.Version, e.Manifest.Version, Version)
	}
	return e, nil
}

// NewManifestV1 creates a new ManifestV1 with the current timestamp.
func NewManifestV1(eventID, model, status string, cost float64, parentEvents []string) ManifestV1 {
	return ManifestV1{
		Version:      Version,
		EventID:      eventID,
		Timestamp:    time.Now().UnixNano(),
		Model:        model,
		Cost:         cost,
		Status:       status,
		ParentEvents: parentEvents,
	}
}

// NewEventV1 creates a new EventV1 embedding the provided manifest.
func NewEventV1(eventID string, manifest ManifestV1, request, response, meta json.RawMessage) EventV1 {
	return EventV1{
		Version:  Version,
		EventID:  eventID,
		Manifest: manifest,
		Request:  request,
		Response: response,
		Meta:     meta,
	}
}
