package consult

// SerializerVersionV1 is the canonical serializer version for consult manifests.
const SerializerVersionV1 = "consult_manifest.v1"

// EventSchemaVersionV1 is the canonical schema version for durable consult events.
const EventSchemaVersionV1 = "consult_event.v1"

const (
	OutcomeResolved   = "resolved"
	OutcomeUnresolved = "unresolved"
	RoleLeader        = "leader"
	RoleFollower      = "follower"
)

// SelectedItem is the compact, model-facing item shape selected for consult.
type SelectedItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref,omitempty"`
	SummaryRef string `json:"summary_ref,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
}

// Input is the normalized consult boundary used to build a deterministic manifest.
type Input struct {
	SessionID     string
	BranchID      string
	Fingerprint   string
	Mode          string
	Scope         string
	Refs          []string
	Constraints   map[string]string
	SelectedItems []SelectedItem
	ByteBudget    int
	Compacted     bool
	Truncated     bool
	TaskSummary   string
}

// Manifest is the deterministic, human-reviewable consult artifact.
type Manifest struct {
	SessionID         string            `json:"session_id"`
	BranchID          string            `json:"branch_id"`
	Fingerprint       string            `json:"fingerprint"`
	Mode              string            `json:"mode"`
	Scope             string            `json:"scope,omitempty"`
	Refs              []string          `json:"refs,omitempty"`
	Constraints       map[string]string `json:"constraints,omitempty"`
	SelectedItems     []SelectedItem    `json:"selected_items,omitempty"`
	ByteBudget        int               `json:"byte_budget"`
	ActualBytes       int               `json:"actual_bytes"`
	Compacted         bool              `json:"compacted"`
	Truncated         bool              `json:"truncated"`
	SerializerVersion string            `json:"serializer_version"`
	TaskSummary       string            `json:"task_summary,omitempty"`
}

// EventSummary is the narrow durable consult record stored on the event spine.
// It captures replayable consult facts without persisting the full artifact.
type EventSummary struct {
	SchemaVersion             string
	Fingerprint               string
	ContractVersion           string
	ManifestSerializerVersion string
	Outcome                   string
	Role                      string
	LeaderConsultEventID      string
	ByteBudget                int
	ActualBytes               int
	TaskSummary               string
	Missing                   []string
}

// Meta projects the durable consult summary into event meta.
func (e EventSummary) Meta() map[string]any {
	meta := map[string]any{
		"schema_version":   e.SchemaVersion,
		"fingerprint":      e.Fingerprint,
		"contract_version": e.ContractVersion,
		"outcome":          e.Outcome,
		"role":             e.Role,
		"byte_budget":      e.ByteBudget,
		"actual_bytes":     e.ActualBytes,
	}
	if e.ManifestSerializerVersion != "" {
		meta["manifest_serializer_version"] = e.ManifestSerializerVersion
	}
	if e.LeaderConsultEventID != "" {
		meta["leader_consult_event_id"] = e.LeaderConsultEventID
	}
	if e.TaskSummary != "" {
		meta["task_summary"] = e.TaskSummary
	}
	if len(e.Missing) > 0 {
		meta["missing"] = append([]string(nil), e.Missing...)
	}
	return meta
}
