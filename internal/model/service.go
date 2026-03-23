package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
)

func Execute(ctx context.Context, adapter Adapter, req Request) (CallResult, error) {
	if err := req.Validate(); err != nil {
		return CallResult{}, err
	}

	raw, err := adapter.StatelessCall(ctx, req)
	if err != nil {
		return CallResult{}, err
	}
	resp, err := ParseAndValidateResponse(raw)
	if err != nil {
		return CallResult{}, err
	}
	return CallResult{Response: resp, Raw: raw}, nil
}

func ExecuteAndPersist(ctx context.Context, b backend.Backend, adapter Adapter, req Request, eventID string, ts time.Time) (CallResult, error) {
	result, err := Execute(ctx, adapter, req)
	if err != nil {
		if errors.Is(err, ErrMalformedModelResponse) {
			invalid := eventlog.Event{
				ID:        eventID + ".invalid",
				SessionID: req.SessionID,
				TS:        ts,
				Kind:      eventlog.KindResponseInvalid,
				BranchID:  req.BranchID,
				Meta: map[string]any{
					"adapter": adapter.Name(),
					"error":   err.Error(),
				},
			}
			if appendErr := b.AppendEvent(ctx, invalid); appendErr != nil {
				return CallResult{}, fmt.Errorf("%w: %v", ErrBackendPersistFailed, appendErr)
			}
		}
		return CallResult{}, err
	}

	blobRef := fmt.Sprintf("blob://model/%s/%s", req.SessionID, eventID)
	if err := b.AppendBlob(ctx, blobRef, result.Raw); err != nil {
		return CallResult{}, fmt.Errorf("%w: %v", ErrBackendPersistFailed, err)
	}
	received := eventlog.Event{
		ID:        eventID + ".received",
		SessionID: req.SessionID,
		TS:        ts,
		Kind:      eventlog.KindResponseReceived,
		BranchID:  req.BranchID,
		BlobRef:   blobRef,
		Meta: map[string]any{
			"adapter":         adapter.Name(),
			"decision":        result.Response.Decision,
			"unresolved_refs": len(result.Response.UnresolvedRefs),
		},
	}
	if err := b.AppendEvent(ctx, received); err != nil {
		return CallResult{}, fmt.Errorf("%w: %v", ErrBackendPersistFailed, err)
	}
	return result, nil
}
