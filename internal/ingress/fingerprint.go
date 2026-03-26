package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type canonicalConstraint struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type canonicalFingerprint struct {
	SessionID       string                `json:"session_id"`
	BranchID        string                `json:"branch_id"`
	Task            string                `json:"task"`
	Refs            []string              `json:"refs,omitempty"`
	Constraints     []canonicalConstraint `json:"constraints,omitempty"`
	AllowTools      bool                  `json:"allow_tools"`
	ExternalContext string                `json:"external_context,omitempty"`
}

// Fingerprint returns a deterministic digest for logically equivalent ingress requests.
// Canonicalization rules:
// - refs are sorted
// - constraints are sorted by key
// - stable JSON encoding is hashed
// - transient fields are excluded by input shape
func Fingerprint(in FingerprintInput) (string, error) {
	canonical := canonicalFingerprint{
		SessionID:       in.SessionID,
		BranchID:        in.BranchID,
		Task:            in.Task,
		AllowTools:      in.AllowTools,
		ExternalContext: normalizeExternalContext(in.ExternalContext),
	}

	if len(in.Refs) > 0 {
		canonical.Refs = append([]string(nil), in.Refs...)
		sort.Strings(canonical.Refs)
	}

	if len(in.Constraints) > 0 {
		canonical.Constraints = make([]canonicalConstraint, 0, len(in.Constraints))
		for k, v := range in.Constraints {
			canonical.Constraints = append(canonical.Constraints, canonicalConstraint{Key: k, Value: v})
		}
		sort.Slice(canonical.Constraints, func(i, j int) bool {
			if canonical.Constraints[i].Key == canonical.Constraints[j].Key {
				return canonical.Constraints[i].Value < canonical.Constraints[j].Value
			}
			return canonical.Constraints[i].Key < canonical.Constraints[j].Key
		})
	}

	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// FingerprintRequest computes a deterministic fingerprint directly from Request.
func FingerprintRequest(req Request) (string, error) {
	return Fingerprint(req.FingerprintInput())
}
