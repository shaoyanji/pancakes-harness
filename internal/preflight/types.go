package preflight

// Input is the boundary payload from ingress-derived intent.
type Input struct {
	Mode           string
	Scope          string
	AllowExecution bool
	AllowTools     bool
	Refs           []string
	Constraints    map[string]string
	Reason         string
}

// Result is the typed preflight object used for downstream routing decisions.
// Resolved=false with Missing populated is a valid non-error state.
type Result struct {
	Mode           string            `json:"mode"`
	Resolved       bool              `json:"resolved"`
	Missing        []string          `json:"missing,omitempty"`
	Scope          string            `json:"scope,omitempty"`
	AllowExecution bool              `json:"allow_execution"`
	AllowTools     bool              `json:"allow_tools"`
	Refs           []string          `json:"refs,omitempty"`
	Constraints    map[string]string `json:"constraints,omitempty"`
	Reason         string            `json:"reason,omitempty"`
}
