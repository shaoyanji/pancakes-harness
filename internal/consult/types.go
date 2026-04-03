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
	Reason     string `json:"reason,omitempty"`
}

type SelectionItem struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Class  string `json:"class,omitempty"`
}

type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type SelectionExplanation struct {
	Included                 []SelectionItem `json:"included,omitempty"`
	Excluded                 []SelectionItem `json:"excluded,omitempty"`
	DominantInclusionReasons []ReasonCount   `json:"dominant_inclusion_reasons,omitempty"`
	DominantExclusionReasons []ReasonCount   `json:"dominant_exclusion_reasons,omitempty"`
	BudgetPressure           bool            `json:"budget_pressure,omitempty"`
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
	Selection     *SelectionExplanation
}

// Manifest is the deterministic, human-reviewable consult artifact.
type Manifest struct {
	SessionID         string                `json:"session_id"`
	BranchID          string                `json:"branch_id"`
	Fingerprint       string                `json:"fingerprint"`
	Mode              string                `json:"mode"`
	Scope             string                `json:"scope,omitempty"`
	Refs              []string              `json:"refs,omitempty"`
	Constraints       map[string]string     `json:"constraints,omitempty"`
	SelectedItems     []SelectedItem        `json:"selected_items,omitempty"`
	ByteBudget        int                   `json:"byte_budget"`
	ActualBytes       int                   `json:"actual_bytes"`
	Compacted         bool                  `json:"compacted"`
	Truncated         bool                  `json:"truncated"`
	SerializerVersion string                `json:"serializer_version"`
	TaskSummary       string                `json:"task_summary,omitempty"`
	Selection         *SelectionExplanation `json:"selection,omitempty"`
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
	Selection                 *SelectionExplanation
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
	if e.Selection != nil {
		meta["selection"] = e.Selection.Meta()
	}
	return meta
}

func (s *SelectionExplanation) Meta() map[string]any {
	if s == nil {
		return nil
	}
	meta := map[string]any{}
	if len(s.Included) > 0 {
		items := make([]map[string]any, 0, len(s.Included))
		for _, item := range s.Included {
			entry := map[string]any{
				"id":     item.ID,
				"kind":   item.Kind,
				"reason": item.Reason,
			}
			if item.Class != "" {
				entry["class"] = item.Class
			}
			items = append(items, entry)
		}
		meta["included"] = items
	}
	if len(s.Excluded) > 0 {
		items := make([]map[string]any, 0, len(s.Excluded))
		for _, item := range s.Excluded {
			entry := map[string]any{
				"id":     item.ID,
				"kind":   item.Kind,
				"reason": item.Reason,
			}
			if item.Class != "" {
				entry["class"] = item.Class
			}
			items = append(items, entry)
		}
		meta["excluded"] = items
	}
	if len(s.DominantInclusionReasons) > 0 {
		meta["dominant_inclusion_reasons"] = selectionReasonMeta(s.DominantInclusionReasons)
	}
	if len(s.DominantExclusionReasons) > 0 {
		meta["dominant_exclusion_reasons"] = selectionReasonMeta(s.DominantExclusionReasons)
	}
	if s.BudgetPressure {
		meta["budget_pressure"] = true
	}
	return meta
}

func selectionReasonMeta(in []ReasonCount) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, reason := range in {
		out = append(out, map[string]any{
			"reason": reason.Reason,
			"count":  reason.Count,
		})
	}
	return out
}
