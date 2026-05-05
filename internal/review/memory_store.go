package review

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"pancakes-harness/internal/consult"
)

// ErrNotFound is returned when a requested event or manifest is not found.
var ErrNotFound = errors.New("not found")

// MemoryStore is an in-memory implementation of consultStore for testing.
type MemoryStore struct {
	mu       sync.RWMutex
	events   map[string]consult.EventV1
	manifests []consult.ManifestV1
}

// NewMemoryStore creates a new in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:    make(map[string]consult.EventV1),
		manifests: make([]consult.ManifestV1, 0),
	}
}

// SaveEvent stores an event in memory.
func (s *MemoryStore) SaveEvent(ctx context.Context, e consult.EventV1) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[e.EventID] = e
	s.manifests = append(s.manifests, e.Manifest)
	return nil
}

// ListManifests returns manifests with pagination.
func (s *MemoryStore) ListManifests(ctx context.Context, limit, offset int) ([]consult.ManifestV1, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = len(s.manifests)
	}

	start := offset
	if start > len(s.manifests) {
		return []consult.ManifestV1{}, nil
	}

	end := start + limit
	if end > len(s.manifests) {
		end = len(s.manifests)
	}

	result := make([]consult.ManifestV1, end-start)
	copy(result, s.manifests[start:end])
	return result, nil
}

// GetEvent retrieves an event by ID.
func (s *MemoryStore) GetEvent(ctx context.Context, eventID string) (consult.EventV1, error) {
	select {
	case <-ctx.Done():
		return consult.EventV1{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.events[eventID]
	if !ok {
		return consult.EventV1{}, ErrNotFound
	}
	return e, nil
}

// StreamManifests returns a channel of all manifests.
func (s *MemoryStore) StreamManifests(ctx context.Context) (<-chan consult.ManifestV1, <-chan error) {
	ch := make(chan consult.ManifestV1)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)

		s.mu.RLock()
		manifests := make([]consult.ManifestV1, len(s.manifests))
		copy(manifests, s.manifests)
		s.mu.RUnlock()

		for _, m := range manifests {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case ch <- m:
			}
		}
		errCh <- nil
	}()

	return ch, errCh
}

// AddTestEvents adds test events to the store.
func (s *MemoryStore) AddTestEvents(events []consult.EventV1) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range events {
		s.events[e.EventID] = e
		s.manifests = append(s.manifests, e.Manifest)
	}
	return nil
}

// Count returns the number of stored events.
func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}

// CreateTestEvent creates a test event for use in tests.
func CreateTestEvent(eventID, model, status string) consult.EventV1 {
	manifest := consult.ManifestV1{
		Version:   consult.Version,
		EventID:   eventID,
		Model:     model,
		Status:    status,
		Timestamp: 1700000000000000000,
	}

	return consult.EventV1{
		Version:  consult.Version,
		EventID:  eventID,
		Manifest: manifest,
		Request:  json.RawMessage(`{"query":"test"}`),
		Response: json.RawMessage(`{"answer":"response"}`),
	}
}
