package replay

import (
	"context"
	"errors"
	"fmt"

	"pancakes-harness/internal/branchdag"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/summaries"
)

var ErrMixedSessionReplay = errors.New("replay input contains multiple sessions")
var ErrSummaryBasisNotFound = errors.New("summary basis event not found in replay input")

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
