package assembler

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

func Assemble(req Request) (Result, error) {
	normalized := normalizeRequest(req)
	body := normalized.Body

	headerBytes := measureHeaders(normalized.Headers)
	requestLineBytes := len(normalized.Method) + 1 + len(normalized.Path) + len(" HTTP/1.1\r\n")
	bodyBudget := MaxEnvelopeBytes - SafetyMarginBytes - requestLineBytes - headerBytes
	if bodyBudget < 0 {
		bodyBudget = 0
	}

	for stage := 0; stage <= 7; stage++ {
		body.CompactStage = stage
		bodyJSON, bodyBytes := marshalDeterministicBody(body)
		envelopeBytes := requestLineBytes + headerBytes + bodyBytes

		if bodyBytes <= bodyBudget && envelopeBytes <= MaxEnvelopeBytes {
			return Result{
				BodyJSON: bodyJSON,
				Stage:    stage,
				Body:     body,
				Measurement: Measurement{
					RequestLineBytes: requestLineBytes,
					HeaderBytes:      headerBytes,
					BodyBudgetBytes:  bodyBudget,
					BodyBytes:        bodyBytes,
					EnvelopeBytes:    envelopeBytes,
				},
			}, nil
		}

		if stage == 7 {
			return Result{
				BodyJSON: bodyJSON,
				Stage:    stage,
				Body:     body,
				Measurement: Measurement{
					RequestLineBytes: requestLineBytes,
					HeaderBytes:      headerBytes,
					BodyBudgetBytes:  bodyBudget,
					BodyBytes:        bodyBytes,
					EnvelopeBytes:    envelopeBytes,
				},
			}, ErrPacketRejectedBudget
		}

		body = applyCompactionStage(body, stage+1)
	}

	return Result{}, ErrPacketRejectedBudget
}

func normalizeRequest(req Request) Request {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "POST"
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/v1/responses"
	}

	headers := append([]Header(nil), req.Headers...)
	sort.Slice(headers, func(i, j int) bool {
		if headers[i].Name == headers[j].Name {
			return headers[i].Value < headers[j].Value
		}
		return headers[i].Name < headers[j].Name
	})

	body := cloneBody(req.Body)
	sort.Slice(body.WorkingSet, func(i, j int) bool {
		if body.WorkingSet[i].FrontierOrdinal == body.WorkingSet[j].FrontierOrdinal {
			return body.WorkingSet[i].ID < body.WorkingSet[j].ID
		}
		return body.WorkingSet[i].FrontierOrdinal < body.WorkingSet[j].FrontierOrdinal
	})
	sort.Strings(body.Frontier)
	sort.Strings(body.Debug)
	sort.Strings(body.Provenance)
	sort.Slice(body.Constraints, func(i, j int) bool {
		if body.Constraints[i].Name == body.Constraints[j].Name {
			return body.Constraints[i].Value < body.Constraints[j].Value
		}
		return body.Constraints[i].Name < body.Constraints[j].Name
	})

	for i := range body.WorkingSet {
		if body.WorkingSet[i].BlobRef != "" && len(body.WorkingSet[i].Text) > LargeTextInlineThresholdBytes {
			body.WorkingSet[i].Text = ""
		}
	}

	return Request{
		Method:  method,
		Path:    path,
		Headers: headers,
		Body:    body,
	}
}

func cloneBody(in PacketBody) PacketBody {
	out := in
	out.WorkingSet = append([]WorkingItem(nil), in.WorkingSet...)
	out.Frontier = append([]string(nil), in.Frontier...)
	out.Debug = append([]string(nil), in.Debug...)
	out.Provenance = append([]string(nil), in.Provenance...)
	out.Constraints = append([]Constraint(nil), in.Constraints...)
	return out
}

func measureHeaders(headers []Header) int {
	total := 0
	for _, h := range headers {
		total += len(h.Name) + len(": ") + len(h.Value) + len("\r\n")
	}
	total += len("\r\n")
	return total
}

func marshalDeterministicBody(body PacketBody) ([]byte, int) {
	b, _ := json.Marshal(body)
	return b, len(b)
}

func applyCompactionStage(body PacketBody, stage int) PacketBody {
	out := cloneBody(body)

	switch stage {
	case 1:
		// remove debug fields
		out.Debug = nil
	case 2:
		// drop non-essential provenance
		filteredItems := make([]WorkingItem, 0, len(out.WorkingSet))
		for _, item := range out.WorkingSet {
			if !item.ProvenanceRequired {
				item.Provenance = ""
			}
			filteredItems = append(filteredItems, item)
		}
		out.WorkingSet = filteredItems
		out.Provenance = nil
	case 3:
		// replace raw excerpts with summary refs
		for i := range out.WorkingSet {
			if out.WorkingSet[i].SummaryRef != "" {
				out.WorkingSet[i].Text = ""
			}
		}
	case 4:
		// collapse multiple deltas into a checkpoint summary
		if out.CheckpointSummaryRef != "" && len(out.WorkingSet) > 1 {
			out.WorkingSet = []WorkingItem{
				{
					ID:         "checkpoint",
					Kind:       "summary.checkpoint",
					SummaryRef: out.CheckpointSummaryRef,
				},
			}
		}
	case 5:
		// shrink working set to newest unresolved frontier
		if len(out.WorkingSet) > 1 {
			newestIdx := 0
			for i := 1; i < len(out.WorkingSet); i++ {
				if out.WorkingSet[i].FrontierOrdinal > out.WorkingSet[newestIdx].FrontierOrdinal {
					newestIdx = i
				}
			}
			out.WorkingSet = []WorkingItem{out.WorkingSet[newestIdx]}
		}
	case 6:
		// replace large text with blob refs only
		for i := range out.WorkingSet {
			if out.WorkingSet[i].BlobRef != "" {
				out.WorkingSet[i].Text = ""
			}
		}
	case 7:
		// final hard failure (no mutation, caller rejects)
	default:
	}

	return out
}

func BodyContainsText(payload []byte, text string) bool {
	return bytes.Contains(payload, []byte(text))
}
