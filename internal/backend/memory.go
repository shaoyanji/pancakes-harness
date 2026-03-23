package backend

import (
	"context"
	"sync"

	"pancakes-harness/internal/eventlog"
)

type MemoryBackend struct {
	mu       sync.RWMutex
	sessions map[string][]eventlog.Event
	byID     map[string]map[string]eventlog.Event
	blobs    map[string][]byte
	diag     []Diagnostic
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		sessions: make(map[string][]eventlog.Event),
		byID:     make(map[string]map[string]eventlog.Event),
		blobs:    make(map[string][]byte),
	}
}

func (b *MemoryBackend) AppendEvent(ctx context.Context, e eventlog.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := e.Validate(); err != nil {
		return ErrInvalidEvent
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.byID[e.SessionID] == nil {
		b.byID[e.SessionID] = make(map[string]eventlog.Event)
	}
	if _, exists := b.byID[e.SessionID][e.ID]; exists {
		return ErrDuplicateEventID
	}
	copied := cloneEvent(e)
	b.sessions[e.SessionID] = append(b.sessions[e.SessionID], copied)
	b.byID[e.SessionID][e.ID] = copied
	return nil
}

func (b *MemoryBackend) AppendBlob(ctx context.Context, ref string, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if ref == "" {
		return ErrBlobNotFound
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.blobs[ref]; exists {
		return ErrDuplicateBlobRef
	}
	b.blobs[ref] = append([]byte(nil), payload...)
	return nil
}

func (b *MemoryBackend) GetEventByID(ctx context.Context, sessionID, eventID string) (eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return eventlog.Event{}, ctx.Err()
	default:
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	events, ok := b.byID[sessionID]
	if !ok {
		return eventlog.Event{}, ErrEventNotFound
	}
	e, ok := events[eventID]
	if !ok {
		return eventlog.Event{}, ErrEventNotFound
	}
	return cloneEvent(e), nil
}

func (b *MemoryBackend) ListEventsBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	stored := b.sessions[sessionID]
	out := make([]eventlog.Event, 0, len(stored))
	for _, e := range stored {
		out = append(out, cloneEvent(e))
	}
	return out, nil
}

func (b *MemoryBackend) ListEventsByBranch(ctx context.Context, sessionID, branchID string) ([]eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if branchID == "" {
		return nil, ErrInvalidBranchRead
	}
	all, err := b.ListEventsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]eventlog.Event, 0)
	for _, e := range all {
		if e.BranchID == branchID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (b *MemoryBackend) FetchBlob(ctx context.Context, ref string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	data, ok := b.blobs[ref]
	if !ok {
		return nil, ErrBlobNotFound
	}
	return append([]byte(nil), data...), nil
}

func (b *MemoryBackend) HealthCheck(ctx context.Context) HealthStatus {
	select {
	case <-ctx.Done():
		return HealthStatus{
			OK: false,
			Diagnostics: []Diagnostic{
				{Code: "context_canceled", Message: ctx.Err().Error()},
			},
		}
	default:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.diag = nil
	return HealthStatus{OK: true}
}

func (b *MemoryBackend) LastDiagnostics() []Diagnostic {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return cloneDiagnostics(b.diag)
}

func (b *MemoryBackend) ClearDiagnostics() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.diag = nil
}

func cloneEvent(in eventlog.Event) eventlog.Event {
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

func cloneDiagnostics(in []Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		cp := d
		if d.Details != nil {
			cp.Details = make(map[string]string, len(d.Details))
			for k, v := range d.Details {
				cp.Details[k] = v
			}
		}
		out = append(out, cp)
	}
	return out
}
