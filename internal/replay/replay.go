package replay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"pancakes-harness/internal/branchdag"
	"pancakes-harness/internal/consult"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/summaries"
)

var ErrMixedSessionReplay = errors.New("replay input contains multiple sessions")
var ErrSummaryBasisNotFound = errors.New("summary basis event not found in replay input")

// ConsultEvent is a summary-grade replay fact for a single consult outcome.
// It is intentionally narrow: enough to replay, review, or export the consult
// later without re-executing the full step or dumping the full artifact.
type ConsultEvent struct {
	EventID                   string                        `json:"event_id"`
	SessionID                 string                        `json:"session_id"`
	BranchID                  string                        `json:"branch_id"`
	Kind                      string                        `json:"kind"`
	SchemaVersion             string                        `json:"schema_version,omitempty"`
	Outcome                   string                        `json:"outcome"`
	Role                      string                        `json:"role,omitempty"`
	Fingerprint               string                        `json:"fingerprint,omitempty"`
	ContractVersion           string                        `json:"contract_version,omitempty"`
	ManifestSerializerVersion string                        `json:"manifest_serializer_version,omitempty"`
	LeaderConsultEventID      string                        `json:"leader_consult_event_id,omitempty"`
	Refs                      []string                      `json:"refs,omitempty"`
	Missing                   []string                      `json:"missing,omitempty"`
	ByteBudget                int                           `json:"byte_budget,omitempty"`
	ActualBytes               int                           `json:"actual_bytes,omitempty"`
	TaskSummary               string                        `json:"task_summary,omitempty"`
	Selection                 *consult.SelectionExplanation `json:"selection,omitempty"`
}

type SessionState struct {
	SessionID   string
	BranchHeads map[string]string
	LastEventID string
	EventCount  int
}

func RebuildSession(events []eventlog.Event) (SessionState, error) {
	state := SessionState{
		BranchHeads: make(map[string]string),
	}
	if len(events) == 0 {
		return state, nil
	}

	state.SessionID = events[0].SessionID
	for _, e := range events {
		if e.SessionID != state.SessionID {
			return SessionState{}, ErrMixedSessionReplay
		}
		if err := e.Validate(); err != nil {
			return SessionState{}, err
		}
		state.BranchHeads[e.BranchID] = e.ID
		state.LastEventID = e.ID
		state.EventCount++
	}
	return state, nil
}

func RebuildFromStore(ctx context.Context, store eventlog.Store, sessionID string) (SessionState, error) {
	events, err := store.ListBySession(ctx, sessionID)
	if err != nil {
		return SessionState{}, err
	}
	return RebuildSession(events)
}

func RebuildBranchStateFromEvents(events []eventlog.Event) (map[string]branchdag.Branch, error) {
	graph := branchdag.NewGraph()

	for _, e := range events {
		if err := e.Validate(); err != nil {
			return nil, err
		}

		if e.Kind == eventlog.KindBranchFork {
			parentBranchID := readMetaString(e.Meta, "parent_branch_id")
			if parentBranchID != "" {
				if _, err := ensureBranch(graph, parentBranchID); err != nil {
					return nil, err
				}
				if _, err := graph.ForkBranch(e.BranchID, parentBranchID, e.ParentEventID); err != nil && !errors.Is(err, branchdag.ErrBranchExists) {
					return nil, err
				}
			} else {
				if _, err := ensureBranch(graph, e.BranchID); err != nil {
					return nil, err
				}
			}
		} else {
			if _, err := ensureBranch(graph, e.BranchID); err != nil {
				return nil, err
			}
		}

		if e.Kind == eventlog.KindSummaryCheckpoint {
			summaryID := readMetaString(e.Meta, "summary_id")
			if summaryID != "" {
				if _, err := graph.SetBaseSummary(e.BranchID, summaryID); err != nil {
					return nil, err
				}
			}
			continue
		}

		_, err := graph.AppendEvent(e.BranchID, e.ID)
		if err != nil {
			return nil, err
		}

		if e.Kind == eventlog.KindSummaryRebuild {
			summaryID := readMetaString(e.Meta, "summary_id")
			if summaryID != "" {
				basisEventID := readMetaString(e.Meta, "basis_event_id")
				if _, err := graph.RebaseOnSummary(e.BranchID, summaryID, basisEventID); err != nil {
					return nil, err
				}
			}
		}
	}

	branches := graph.ListBranches()
	out := make(map[string]branchdag.Branch, len(branches))
	for _, b := range branches {
		out[b.BranchID] = b
	}
	return out, nil
}

func RebuildBranchFromSummaryDelta(branch branchdag.Branch, checkpoint summaries.SummaryCheckpoint, deltaEvents []eventlog.Event) (branchdag.Branch, error) {
	if err := branch.Validate(); err != nil {
		return branchdag.Branch{}, err
	}
	if err := checkpoint.Validate(); err != nil {
		return branchdag.Branch{}, err
	}
	if checkpoint.BranchID != branch.BranchID {
		return branchdag.Branch{}, fmt.Errorf("summary branch %q does not match branch %q", checkpoint.BranchID, branch.BranchID)
	}

	out := branch
	out.BaseSummaryID = checkpoint.SummaryID
	out.HeadEventID = checkpoint.BasisEventID
	out.DirtyRanges = nil

	for _, e := range deltaEvents {
		if err := e.Validate(); err != nil {
			return branchdag.Branch{}, err
		}
		if e.BranchID != branch.BranchID {
			continue
		}
		next, err := branchdag.AppendToBranch(out, e.ID)
		if err != nil {
			return branchdag.Branch{}, err
		}
		out = next
	}
	return out, nil
}

func RebuildBranchFromSummaryAndEvents(branch branchdag.Branch, checkpoint summaries.SummaryCheckpoint, sessionEvents []eventlog.Event) (branchdag.Branch, error) {
	if err := checkpoint.Validate(); err != nil {
		return branchdag.Branch{}, err
	}

	basisIdx := -1
	for i, e := range sessionEvents {
		if e.ID == checkpoint.BasisEventID {
			basisIdx = i
			break
		}
	}
	if basisIdx == -1 {
		return branchdag.Branch{}, ErrSummaryBasisNotFound
	}

	delta := make([]eventlog.Event, 0)
	for _, e := range sessionEvents[basisIdx+1:] {
		if e.BranchID == branch.BranchID {
			delta = append(delta, e)
		}
	}
	return RebuildBranchFromSummaryDelta(branch, checkpoint, delta)
}

// ListConsultEvents extracts consult outcome events from a session's event stream
// in chronological order. It returns both resolved and unresolved consults so that
// the full consult history is visible for replay and review.
func ListConsultEvents(events []eventlog.Event) ([]ConsultEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	sessionID := events[0].SessionID
	var out []ConsultEvent
	for _, e := range events {
		if e.SessionID != sessionID {
			return nil, ErrMixedSessionReplay
		}
		if e.Kind != eventlog.KindConsultResolved && e.Kind != eventlog.KindConsultUnresolved {
			continue
		}
		ce := ConsultEvent{
			EventID:   e.ID,
			SessionID: e.SessionID,
			BranchID:  e.BranchID,
			Kind:      e.Kind,
			Outcome:   consultOutcomeForKind(e.Kind),
			Refs:      append([]string(nil), e.Refs...),
		}
		ce.SchemaVersion = readMetaString(e.Meta, "schema_version")
		ce.Role = readMetaString(e.Meta, "role")
		ce.Fingerprint = readMetaString(e.Meta, "fingerprint")
		ce.ContractVersion = readMetaString(e.Meta, "contract_version")
		ce.ManifestSerializerVersion = readMetaString(e.Meta, "manifest_serializer_version")
		ce.LeaderConsultEventID = readMetaString(e.Meta, "leader_consult_event_id")
		ce.TaskSummary = readMetaString(e.Meta, "task_summary")
		if outcome := readMetaString(e.Meta, "outcome"); outcome != "" {
			ce.Outcome = outcome
		}
		ce.Missing = readMetaStrings(e.Meta, "missing")
		ce.ByteBudget = readMetaInt(e.Meta, "byte_budget")
		ce.ActualBytes = readMetaInt(e.Meta, "actual_bytes")
		ce.Selection = readMetaSelection(e.Meta, "selection")
		out = append(out, ce)
	}
	return out, nil
}

func consultOutcomeForKind(kind string) string {
	switch kind {
	case eventlog.KindConsultResolved:
		return consult.OutcomeResolved
	case eventlog.KindConsultUnresolved:
		return consult.OutcomeUnresolved
	default:
		return ""
	}
}

func ensureBranch(graph *branchdag.Graph, branchID string) (branchdag.Branch, error) {
	if existing, err := graph.GetBranch(branchID); err == nil {
		return existing, nil
	}
	return graph.CreateBranch(branchdag.Branch{BranchID: branchID})
}

func readMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}

func readMetaStrings(meta map[string]any, key string) []string {
	if meta == nil {
		return nil
	}
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, s)
		}
		return out
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		return []string{s}
	default:
		return nil
	}
}

func readMetaInt(meta map[string]any, key string) int {
	if meta == nil {
		return 0
	}
	raw, ok := meta[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func readMetaSelection(meta map[string]any, key string) *consult.SelectionExplanation {
	if meta == nil {
		return nil
	}
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	body, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := &consult.SelectionExplanation{
		Included:                 readSelectionItems(body, "included"),
		Excluded:                 readSelectionItems(body, "excluded"),
		DominantInclusionReasons: readReasonCounts(body, "dominant_inclusion_reasons"),
		DominantExclusionReasons: readReasonCounts(body, "dominant_exclusion_reasons"),
	}
	if pressure, ok := body["budget_pressure"].(bool); ok {
		out.BudgetPressure = pressure
	}
	if len(out.Included) == 0 && len(out.Excluded) == 0 && len(out.DominantInclusionReasons) == 0 && len(out.DominantExclusionReasons) == 0 && !out.BudgetPressure {
		return nil
	}
	return out
}

func readSelectionItems(meta map[string]any, key string) []consult.SelectionItem {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	if list, ok := raw.([]map[string]any); ok {
		out := make([]consult.SelectionItem, 0, len(list))
		for _, item := range list {
			if next, ok := selectionItemFromMeta(item); ok {
				out = append(out, next)
			}
		}
		return out
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]consult.SelectionItem, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if next, ok := selectionItemFromMeta(m); ok {
			out = append(out, next)
		}
	}
	return out
}

func selectionItemFromMeta(meta map[string]any) (consult.SelectionItem, bool) {
	id := readMetaString(meta, "id")
	kind := readMetaString(meta, "kind")
	reason := readMetaString(meta, "reason")
	if id == "" || kind == "" || reason == "" {
		return consult.SelectionItem{}, false
	}
	return consult.SelectionItem{
		ID:     id,
		Kind:   kind,
		Reason: reason,
		Class:  readMetaString(meta, "class"),
	}, true
}

func readReasonCounts(meta map[string]any, key string) []consult.ReasonCount {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	if list, ok := raw.([]map[string]any); ok {
		out := make([]consult.ReasonCount, 0, len(list))
		for _, item := range list {
			reason := readMetaString(item, "reason")
			count := readMetaInt(item, "count")
			if reason == "" || count <= 0 {
				continue
			}
			out = append(out, consult.ReasonCount{Reason: reason, Count: count})
		}
		return out
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]consult.ReasonCount, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		reason := readMetaString(m, "reason")
		count := readMetaInt(m, "count")
		if reason == "" || count <= 0 {
			continue
		}
		out = append(out, consult.ReasonCount{Reason: reason, Count: count})
	}
	return out
}
