package egress

import (
	"sort"
	"strings"
)

const (
	largeTextBytes = 1024
	staleDistance  = 8
	maxExcluded    = 8
)

func Select(candidates []Candidate) []Selected {
	selected, _ := SelectWithExplanation(candidates)
	return selected
}

func SelectWithExplanation(candidates []Candidate) ([]Selected, Explanation) {
	if len(candidates) == 0 {
		return nil, Explanation{}
	}
	latestOrdinal := candidates[len(candidates)-1].FrontierOrdinal
	out := make([]Selected, 0, len(candidates))
	var explanation Explanation
	includedCounts := make(map[ReasonCode]int)
	excludedCounts := make(map[ReasonCode]int)
	for _, c := range candidates {
		sel := Selected{
			ID:              c.ID,
			Kind:            c.Kind,
			Text:            c.Text,
			SummaryRef:      c.SummaryRef,
			BlobRef:         c.BlobRef,
			FrontierOrdinal: c.FrontierOrdinal,
			Include:         true,
			Class:           ClassPassthrough,
			Reason:          ReasonBranchLocality,
		}

		switch {
		case isNeverEgressKind(c.Kind):
			sel.Class = ClassDebugNever
			sel.Include = false
			sel.Reason = ReasonDebugNever
			sel.Text = ""
			sel.SummaryRef = ""
		case !c.IsActiveBranch:
			if c.IsGlobalRelevant {
				sel.Class = ClassRefOnly
				sel.Reason = ReasonGlobalRelevant
				sel.Text = ""
				if sel.BlobRef == "" {
					sel.Include = false
					sel.Class = ClassDropUnlessAsked
					sel.Reason = ReasonRefUnavailable
				}
			} else {
				sel.Class = ClassDropUnlessAsked
				sel.Include = false
				sel.Reason = ReasonNonLocal
				sel.Text = ""
				sel.SummaryRef = ""
			}
		case isDebugNeverKind(c.Kind):
			sel.Class = ClassDebugNever
			sel.Include = false
			sel.Reason = ReasonDebugNever
			sel.Text = ""
			sel.SummaryRef = ""
		case c.IsSensitiveLocal:
			sel.Class = ClassDropUnlessAsked
			sel.Include = false
			sel.Reason = ReasonSensitiveLocal
			sel.Text = ""
			sel.SummaryRef = ""
		case c.IsCurrentUserTurn:
			sel.Class = ClassPassthrough
			sel.Reason = ReasonRecentTurn
		case c.IsLatestToolResult:
			sel.Class = ClassSummaryOnly
			sel.Reason = ReasonToolResult
			sel.Text = ""
			if sel.SummaryRef == "" {
				sel.SummaryRef = c.SummaryRef
			}
		case c.IsNearestSummary:
			sel.Class = ClassSummaryOnly
			sel.Reason = ReasonSummaryCheckpoint
			sel.Text = ""
			if sel.SummaryRef == "" {
				sel.SummaryRef = c.SummaryRef
			}
		case c.IsCheckpoint:
			sel.Class = ClassRefOnly
			sel.Reason = ReasonCheckpointRef
			sel.Text = ""
		case isLargeAndStale(c, latestOrdinal):
			sel.Class = ClassRefOnly
			sel.Reason = ReasonBudgetFit
			sel.Text = ""
			if sel.SummaryRef == "" && sel.BlobRef != "" {
				// keep only ref surface for large stale entries
				sel.SummaryRef = ""
			}
		default:
			sel.Class = ClassPassthrough
			sel.Reason = ReasonBranchLocality
		}
		out = append(out, sel)
		item := ItemReason{
			ID:              sel.ID,
			Kind:            sel.Kind,
			Reason:          sel.Reason,
			Class:           sel.Class,
			FrontierOrdinal: sel.FrontierOrdinal,
		}
		if sel.Include {
			explanation.Included = append(explanation.Included, item)
			includedCounts[sel.Reason]++
			continue
		}
		if len(explanation.Excluded) < maxExcluded {
			explanation.Excluded = append(explanation.Excluded, item)
		}
		excludedCounts[sel.Reason]++
	}
	explanation.DominantInclusionReasons = dominantReasons(includedCounts)
	explanation.DominantExclusionReasons = dominantReasons(excludedCounts)
	return out, explanation
}

func isLargeAndStale(c Candidate, latestOrdinal int) bool {
	if len(c.Text) <= largeTextBytes {
		return false
	}
	if latestOrdinal-c.FrontierOrdinal <= staleDistance {
		return false
	}
	return true
}

func isDebugNeverKind(kind string) bool {
	k := strings.TrimSpace(strings.ToLower(kind))
	if strings.HasPrefix(k, "packet.") || strings.HasPrefix(k, "response.") {
		return true
	}
	if strings.Contains(k, "debug") || strings.Contains(k, "metrics") || strings.Contains(k, "trace") {
		return true
	}
	return false
}

func isNeverEgressKind(kind string) bool {
	k := strings.TrimSpace(strings.ToLower(kind))
	if strings.HasPrefix(k, "branch.fork") {
		return true
	}
	if strings.HasPrefix(k, "consult.") {
		return true
	}
	if strings.HasPrefix(k, "summary.rebuild") {
		return true
	}
	if strings.HasPrefix(k, "replay.") {
		return true
	}
	// New v0.3.0 event kinds are spine-only, never egress.
	if strings.HasPrefix(k, "recovery.") {
		return true
	}
	if strings.HasPrefix(k, "context.compact") {
		return true
	}
	if strings.HasPrefix(k, "dream.") {
		return true
	}
	if strings.HasPrefix(k, "audit.") {
		return true
	}
	return false
}

func dominantReasons(counts map[ReasonCode]int) []ReasonCount {
	if len(counts) == 0 {
		return nil
	}
	out := make([]ReasonCount, 0, len(counts))
	for reason, count := range counts {
		if count <= 0 {
			continue
		}
		out = append(out, ReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
