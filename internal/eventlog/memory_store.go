package eventlog

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string][]Event
	byID     map[string]map[string]Event
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string][]Event),
		byID:     make(map[string]map[string]Event),
	}
}

func (s *MemoryStore) Append(ctx context.Context, e Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := e.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.byID[e.SessionID] == nil {
		s.byID[e.SessionID] = make(map[string]Event)
	}
	if _, exists := s.byID[e.SessionID][e.ID]; exists {
		return ErrDuplicateEventID
	}

	copied := cloneEvent(e)
	s.sessions[e.SessionID] = append(s.sessions[e.SessionID], copied)
	s.byID[e.SessionID][e.ID] = copied
	return nil
}

func (s *MemoryStore) GetByID(ctx context.Context, sessionID, eventID string) (Event, error) {
	select {
	case <-ctx.Done():
		return Event{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	eventsByID, ok := s.byID[sessionID]
	if !ok {
		return Event{}, ErrEventNotFound
	}
	e, ok := eventsByID[eventID]
	if !ok {
		return Event{}, ErrEventNotFound
	}
	return cloneEvent(e), nil
}

func (s *MemoryStore) ListBySession(ctx context.Context, sessionID string) ([]Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	stored := s.sessions[sessionID]
	out := make([]Event, 0, len(stored))
	for _, e := range stored {
		out = append(out, cloneEvent(e))
	}
	return out, nil
}
