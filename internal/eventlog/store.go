package eventlog

import (
	"context"
	"errors"
)

var (
	ErrDuplicateEventID = errors.New("duplicate event id in session")
	ErrEventNotFound    = errors.New("event not found")
)

// Store is the event-log persistence boundary used by runtime and replay code.
type Store interface {
	Append(ctx context.Context, e Event) error
	GetByID(ctx context.Context, sessionID, eventID string) (Event, error)
	ListBySession(ctx context.Context, sessionID string) ([]Event, error)
}
