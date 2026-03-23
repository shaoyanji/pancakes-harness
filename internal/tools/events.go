package tools

import (
	"sort"
	"strconv"
	"time"

	"pancakes-harness/internal/eventlog"
)

func ToolRequestEvent(id, sessionID, branchID string, ts time.Time, req Request) eventlog.Event {
	return eventlog.Event{
		ID:        id,
		SessionID: sessionID,
		TS:        ts,
		Kind:      eventlog.KindToolRequest,
		BranchID:  branchID,
		Meta: map[string]any{
			"tool":       req.Tool,
			"call_id":    req.CallID,
			"timeout_ms": req.TimeoutMS,
			"args_json":  mustJSON(req.Args),
		},
	}
}

func ToolResultEvent(id, sessionID, branchID string, ts time.Time, req Request, resp Response) eventlog.Event {
	refs := make([]string, 0, len(resp.Artifacts))
	blobRef := ""
	for _, a := range resp.Artifacts {
		if a.BlobRef == "" {
			continue
		}
		if blobRef == "" {
			blobRef = a.BlobRef
		}
		refs = append(refs, a.BlobRef)
	}
	sort.Strings(refs)
	return eventlog.Event{
		ID:        id,
		SessionID: sessionID,
		TS:        ts,
		Kind:      eventlog.KindToolResult,
		BranchID:  branchID,
		Refs:      refs,
		BlobRef:   blobRef,
		Meta: map[string]any{
			"tool":           req.Tool,
			"call_id":        req.CallID,
			"summary":        resp.Summary,
			"result_json":    mustJSON(resp.Result),
			"artifact_count": strconv.Itoa(len(resp.Artifacts)),
		},
	}
}

func ToolFailureEvent(id, sessionID, branchID string, ts time.Time, req Request, resp Response) eventlog.Event {
	typ := ErrorTypeTool
	msg := "tool failure"
	if resp.Error != nil {
		typ = resp.Error.Type
		msg = resp.Error.Message
	}
	return eventlog.Event{
		ID:        id,
		SessionID: sessionID,
		TS:        ts,
		Kind:      eventlog.KindToolFailure,
		BranchID:  branchID,
		Meta: map[string]any{
			"tool":          req.Tool,
			"call_id":       req.CallID,
			"error_type":    typ,
			"error_message": msg,
		},
	}
}
