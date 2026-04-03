package consult

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var ErrMalformedInput = errors.New("malformed consult input")

// Generate builds a deterministic consult manifest from normalized boundary input.
func Generate(in Input) (Manifest, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	branchID := strings.TrimSpace(in.BranchID)
	fingerprint := strings.TrimSpace(in.Fingerprint)
	mode := strings.TrimSpace(in.Mode)
	scope := strings.TrimSpace(in.Scope)
	taskSummary := strings.TrimSpace(in.TaskSummary)

	if sessionID == "" || branchID == "" || fingerprint == "" || mode == "" {
		return Manifest{}, ErrMalformedInput
	}
	if in.ByteBudget < 0 {
		return Manifest{}, ErrMalformedInput
	}

	refs := normalizeRefs(in.Refs)
	constraints, ok := normalizeConstraints(in.Constraints)
	if !ok {
		return Manifest{}, ErrMalformedInput
	}
	items, ok := normalizeSelectedItems(in.SelectedItems)
	if !ok {
		return Manifest{}, ErrMalformedInput
	}
	selection, ok := normalizeSelectionExplanation(in.Selection)
	if !ok {
		return Manifest{}, ErrMalformedInput
	}

	manifest := Manifest{
		SessionID:         sessionID,
		BranchID:          branchID,
		Fingerprint:       fingerprint,
		Mode:              mode,
		Scope:             scope,
		Refs:              refs,
		Constraints:       constraints,
		SelectedItems:     items,
		ByteBudget:        in.ByteBudget,
		Compacted:         in.Compacted,
		Truncated:         in.Truncated,
		SerializerVersion: SerializerVersionV1,
		TaskSummary:       taskSummary,
		Selection:         selection,
	}

	actual, err := measuredBytes(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.ActualBytes = actual

	// If the consult artifact exceeds budget, mark compacted explicitly.
	if manifest.ByteBudget > 0 && manifest.ActualBytes > manifest.ByteBudget {
		manifest.Compacted = true
		actual, err = measuredBytes(manifest)
		if err != nil {
			return Manifest{}, err
		}
		manifest.ActualBytes = actual
	}

	return manifest, nil
}

// Marshal deterministically serializes a generated manifest.
func Marshal(m Manifest) ([]byte, error) {
	return json.Marshal(m)
}

func normalizeRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		r := strings.TrimSpace(ref)
		if r == "" {
			continue
		}
		out = append(out, r)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeConstraints(in map[string]string) (map[string]string, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			return nil, false
		}
		out[key] = strings.TrimSpace(v)
	}
	return out, true
}

func normalizeSelectedItems(in []SelectedItem) ([]SelectedItem, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]SelectedItem, 0, len(in))
	for _, item := range in {
		id := strings.TrimSpace(item.ID)
		kind := strings.TrimSpace(item.Kind)
		if id == "" || kind == "" || item.Bytes < 0 {
			return nil, false
		}
		out = append(out, SelectedItem{
			ID:         id,
			Kind:       kind,
			Ref:        strings.TrimSpace(item.Ref),
			SummaryRef: strings.TrimSpace(item.SummaryRef),
			Bytes:      item.Bytes,
			Reason:     strings.TrimSpace(item.Reason),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		if out[i].SummaryRef != out[j].SummaryRef {
			return out[i].SummaryRef < out[j].SummaryRef
		}
		if out[i].Reason != out[j].Reason {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Bytes < out[j].Bytes
	})
	return out, true
}

func normalizeSelectionExplanation(in *SelectionExplanation) (*SelectionExplanation, bool) {
	if in == nil {
		return nil, true
	}
	included, ok := normalizeSelectionItems(in.Included)
	if !ok {
		return nil, false
	}
	excluded, ok := normalizeSelectionItems(in.Excluded)
	if !ok {
		return nil, false
	}
	dominantIncluded, ok := normalizeReasonCounts(in.DominantInclusionReasons)
	if !ok {
		return nil, false
	}
	dominantExcluded, ok := normalizeReasonCounts(in.DominantExclusionReasons)
	if !ok {
		return nil, false
	}
	return &SelectionExplanation{
		Included:                 included,
		Excluded:                 excluded,
		DominantInclusionReasons: dominantIncluded,
		DominantExclusionReasons: dominantExcluded,
		BudgetPressure:           in.BudgetPressure,
	}, true
}

func normalizeSelectionItems(in []SelectionItem) ([]SelectionItem, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]SelectionItem, 0, len(in))
	for _, item := range in {
		id := strings.TrimSpace(item.ID)
		kind := strings.TrimSpace(item.Kind)
		reason := strings.TrimSpace(item.Reason)
		if id == "" || kind == "" || reason == "" {
			return nil, false
		}
		out = append(out, SelectionItem{
			ID:     id,
			Kind:   kind,
			Reason: reason,
			Class:  strings.TrimSpace(item.Class),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Reason != out[j].Reason {
			return out[i].Reason < out[j].Reason
		}
		return out[i].Class < out[j].Class
	})
	return out, true
}

func normalizeReasonCounts(in []ReasonCount) ([]ReasonCount, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]ReasonCount, 0, len(in))
	for _, reason := range in {
		code := strings.TrimSpace(reason.Reason)
		if code == "" || reason.Count <= 0 {
			return nil, false
		}
		out = append(out, ReasonCount{Reason: code, Count: reason.Count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	return out, true
}

func measuredBytes(m Manifest) (int, error) {
	candidate := m
	candidate.ActualBytes = 0
	for i := 0; i < 8; i++ {
		b, err := json.Marshal(candidate)
		if err != nil {
			return 0, err
		}
		n := len(b)
		if candidate.ActualBytes == n {
			return n, nil
		}
		candidate.ActualBytes = n
	}
	return candidate.ActualBytes, nil
}
