package ingress

import "strings"

// Request is the ingress request shape used for controller primitives.
// It intentionally omits transient/debug fields so fingerprints remain stable.
type Request struct {
	SessionID       string            `json:"session_id"`
	BranchID        string            `json:"branch_id"`
	Task            string            `json:"task"`
	Refs            []string          `json:"refs,omitempty"`
	Constraints     map[string]string `json:"constraints,omitempty"`
	AllowTools      bool              `json:"allow_tools"`
	ExternalContext string            `json:"external_context,omitempty"`
}

// FingerprintInput is the canonicalizable input used to compute request fingerprints.
// Keeping this separate allows future ingress envelopes to add non-fingerprint fields.
type FingerprintInput struct {
	SessionID       string
	BranchID        string
	Task            string
	Refs            []string
	Constraints     map[string]string
	AllowTools      bool
	ExternalContext string
}

// FingerprintInput returns a fingerprint-safe projection of the request.
func (r Request) FingerprintInput() FingerprintInput {
	return FingerprintInput{
		SessionID:       r.SessionID,
		BranchID:        r.BranchID,
		Task:            r.Task,
		Refs:            r.Refs,
		Constraints:     r.Constraints,
		AllowTools:      r.AllowTools,
		ExternalContext: normalizeExternalContext(r.ExternalContext),
	}
}

// NormalizedExternalContext returns the canonical optional external context.
func (r Request) NormalizedExternalContext() string {
	return normalizeExternalContext(r.ExternalContext)
}

func normalizeExternalContext(in string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return ""
	}
	return trimmed
}
