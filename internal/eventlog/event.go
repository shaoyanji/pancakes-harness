package eventlog

import (
	"errors"
	"time"
)

var ErrInvalidEvent = errors.New("invalid event")

// Event is the canonical append-only record for the local session graph.
type Event struct {
	ID            string
	SessionID     string
	TS            time.Time
	Kind          string
	BranchID      string
	ParentEventID string
	Refs          []string
	Meta          map[string]any
	BlobRef       string
}

func (e Event) Validate() error {
	if e.ID == "" {
		return ErrInvalidEvent
	}
	if e.SessionID == "" {
		return ErrInvalidEvent
	}
	if e.TS.IsZero() {
		return ErrInvalidEvent
	}
	if e.Kind == "" {
		return ErrInvalidEvent
	}
	if e.BranchID == "" {
		return ErrInvalidEvent
	}
	return nil
}

func cloneEvent(in Event) Event {
	out := in
	if in.Refs != nil {
		out.Refs = append([]string(nil), in.Refs...)
	}
	if in.Meta != nil {
		out.Meta = make(map[string]any, len(in.Meta))
		for k, v := range in.Meta {
			out.Meta[k] = v
		}
	}
	return out
}
