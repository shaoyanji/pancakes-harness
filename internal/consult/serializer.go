package consult

import (
	"encoding/json"
	"fmt"
)

// EncodeManifest encodes a consult manifest with version enforcement.
func EncodeManifest(m Manifest) ([]byte, error) {
	if m.SerializerVersion != SerializerVersionV1 {
		return nil, fmt.Errorf("unsupported manifest serializer version %q, expected %q",
			m.SerializerVersion, SerializerVersionV1)
	}
	return json.Marshal(m)
}

// DecodeManifest decodes a consult manifest and enforces version compatibility.
func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest decode: %w", err)
	}
	if m.SerializerVersion != SerializerVersionV1 {
		return m, fmt.Errorf("unsupported manifest serializer version %q, expected %q",
			m.SerializerVersion, SerializerVersionV1)
	}
	return m, nil
}

// EncodeEventSummary encodes a durable consult event summary with version alignment.
func EncodeEventSummary(e EventSummary) ([]byte, error) {
	if e.SchemaVersion != EventSchemaVersionV1 {
		return nil, fmt.Errorf("unsupported event schema version %q, expected %q",
			e.SchemaVersion, EventSchemaVersionV1)
	}
	if e.ManifestSerializerVersion != "" && e.ManifestSerializerVersion != SerializerVersionV1 {
		return nil, fmt.Errorf("event manifest version %q does not align with serializer version %q",
			e.ManifestSerializerVersion, SerializerVersionV1)
	}
	return json.Marshal(e)
}

// DecodeEventSummary decodes a durable consult event summary with version alignment.
func DecodeEventSummary(data []byte) (EventSummary, error) {
	var e EventSummary
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("event summary decode: %w", err)
	}
	if e.SchemaVersion != EventSchemaVersionV1 {
		return e, fmt.Errorf("unsupported event schema version %q, expected %q",
			e.SchemaVersion, EventSchemaVersionV1)
	}
	if e.ManifestSerializerVersion != "" && e.ManifestSerializerVersion != SerializerVersionV1 {
		return e, fmt.Errorf("event manifest version %q does not align with serializer version %q",
			e.ManifestSerializerVersion, SerializerVersionV1)
	}
	return e, nil
}

// VersionInfo returns the current serializer version info for diagnostics.
func VersionInfo() map[string]string {
	return map[string]string{
		"serializer_version":   SerializerVersionV1,
		"event_schema_version": EventSchemaVersionV1,
	}
}
