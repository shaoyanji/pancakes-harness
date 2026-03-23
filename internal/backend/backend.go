package backend

import (
	"context"
	"errors"

	"pancakes-harness/internal/eventlog"
)

var (
	ErrBlobNotFound      = errors.New("blob not found")
	ErrDuplicateBlobRef  = errors.New("duplicate blob ref")
	ErrDuplicateEventID  = errors.New("duplicate event id in session")
	ErrEventNotFound     = errors.New("event not found")
	ErrInvalidEvent      = errors.New("invalid event")
	ErrInvalidBranchRead = errors.New("invalid branch read")
)

type Diagnostic struct {
	Code    string
	Message string
	Details map[string]string
}

type HealthStatus struct {
	OK          bool
	Diagnostics []Diagnostic
}

// Backend is the runtime-facing storage adapter boundary.
// Runtime logic should depend on this interface rather than xs specifics.
type Backend interface {
	AppendEvent(ctx context.Context, e eventlog.Event) error
	AppendBlob(ctx context.Context, ref string, payload []byte) error
	GetEventByID(ctx context.Context, sessionID, eventID string) (eventlog.Event, error)
	ListEventsBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error)
	ListEventsByBranch(ctx context.Context, sessionID, branchID string) ([]eventlog.Event, error)
	FetchBlob(ctx context.Context, ref string) ([]byte, error)
	HealthCheck(ctx context.Context) HealthStatus
	LastDiagnostics() []Diagnostic
	ClearDiagnostics()
}
