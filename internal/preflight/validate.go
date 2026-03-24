package preflight

import (
	"errors"
	"sort"
	"strings"
)

var ErrMalformedInput = errors.New("malformed preflight input")

// Validate converts boundary input into a deterministic typed preflight result.
// States:
// - malformed input: returns ErrMalformedInput
// - valid unresolved intent: returns result with Resolved=false and Missing populated
// - valid resolved intent: returns result with Resolved=true
func Validate(in Input) (Result, error) {
	result := Result{
		Mode:           strings.TrimSpace(in.Mode),
		Scope:          strings.TrimSpace(in.Scope),
		AllowExecution: in.AllowExecution,
		AllowTools:     in.AllowTools,
		Reason:         strings.TrimSpace(in.Reason),
	}

	if result.Mode == "" {
		result.Missing = []string{"mode"}
		if result.Reason == "" {
			result.Reason = "malformed boundary input"
		}
		return result, ErrMalformedInput
	}

	refs, ok := normalizeRefs(in.Refs)
	if !ok {
		result.Reason = "invalid refs"
		return result, ErrMalformedInput
	}
	result.Refs = refs

	constraints, ok := normalizeConstraints(in.Constraints)
	if !ok {
		result.Reason = "invalid constraints"
		return result, ErrMalformedInput
	}
	result.Constraints = constraints

	missing := make([]string, 0, 1)
	if result.Scope == "" {
		missing = append(missing, "scope")
	}
	sort.Strings(missing)
	result.Missing = missing
	result.Resolved = len(missing) == 0

	if result.Reason == "" {
		if result.Resolved {
			result.Reason = "resolved"
		} else {
			result.Reason = "unresolved intent"
		}
	}

	return result, nil
}

func normalizeRefs(refs []string) ([]string, bool) {
	if len(refs) == 0 {
		return nil, true
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		r := strings.TrimSpace(ref)
		if r == "" {
			return nil, false
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out, true
}

func normalizeConstraints(constraints map[string]string) (map[string]string, bool) {
	if len(constraints) == 0 {
		return nil, true
	}
	out := make(map[string]string, len(constraints))
	for k, v := range constraints {
		key := strings.TrimSpace(k)
		if key == "" {
			return nil, false
		}
		out[key] = strings.TrimSpace(v)
	}
	return out, true
}
