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

// SerializedEvent is a lightweight, JSON-friendly representation of an Event
// for external consumption (Gemini compaction, export, etc).
type SerializedEvent struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	BranchID  string `json:"branch_id"`
	TS        string `json:"ts"`     // RFC3339
	Text      string `json:"text,omitempty"`
	Summary   string `json:"summary,omitempty"`
	BlobRef   string `json:"blob_ref,omitempty"`
}

// SerializeForCompaction converts an Event to its compact serialized form.
func SerializeForCompaction(e Event) SerializedEvent {
	se := SerializedEvent{
		ID:       e.ID,
		Kind:     e.Kind,
		BranchID: e.BranchID,
		TS:       e.TS.UTC().Format("2006-01-02T15:04:05Z"),
		BlobRef:  e.BlobRef,
	}
	if e.Meta != nil {
		if t, ok := e.Meta["text"].(string); ok {
			se.Text = t
		}
		if s, ok := e.Meta["summary"].(string); ok {
			se.Summary = s
		}
	}
	return se
}
