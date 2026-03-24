package egress

import "strings"

const (
	largeTextBytes = 1024
	staleDistance  = 8
)

func Select(candidates []Candidate) []Selected {
	if len(candidates) == 0 {
		return nil
	}
	latestOrdinal := candidates[len(candidates)-1].FrontierOrdinal
	out := make([]Selected, 0, len(candidates))
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
		}

		switch {
		case isNeverEgressKind(c.Kind):
			sel.Class = ClassDebugNever
			sel.Include = false
			sel.Text = ""
			sel.SummaryRef = ""
		case !c.IsActiveBranch:
			if c.IsGlobalRelevant {
				sel.Class = ClassRefOnly
				sel.Text = ""
				if sel.BlobRef == "" {
					sel.Include = false
					sel.Class = ClassDropUnlessAsked
				}
			} else {
				sel.Class = ClassDropUnlessAsked
				sel.Include = false
				sel.Text = ""
				sel.SummaryRef = ""
			}
		case isDebugNeverKind(c.Kind):
			sel.Class = ClassDebugNever
			sel.Include = false
			sel.Text = ""
			sel.SummaryRef = ""
		case c.IsSensitiveLocal:
			sel.Class = ClassDropUnlessAsked
			sel.Include = false
			sel.Text = ""
			sel.SummaryRef = ""
		case c.IsCurrentUserTurn:
			sel.Class = ClassPassthrough
		case c.IsLatestToolResult:
			sel.Class = ClassSummaryOnly
			sel.Text = ""
			if sel.SummaryRef == "" {
				sel.SummaryRef = c.SummaryRef
			}
		case c.IsNearestSummary:
			sel.Class = ClassSummaryOnly
			sel.Text = ""
			if sel.SummaryRef == "" {
				sel.SummaryRef = c.SummaryRef
			}
		case c.IsCheckpoint:
			sel.Class = ClassRefOnly
			sel.Text = ""
		case isLargeAndStale(c, latestOrdinal):
			sel.Class = ClassRefOnly
			sel.Text = ""
			if sel.SummaryRef == "" && sel.BlobRef != "" {
				// keep only ref surface for large stale entries
				sel.SummaryRef = ""
			}
		default:
			sel.Class = ClassPassthrough
		}
		out = append(out, sel)
	}
	return out
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
	if strings.HasPrefix(k, "summary.rebuild") {
		return true
	}
	if strings.HasPrefix(k, "replay.") {
		return true
	}
	return false
}
