package review

import (
	"context"
	"errors"
	"sync"

	"pancakes-harness/internal/consult"
)

// ErrNotFound is returned when a requested event or manifest is not found.
var ErrNotFound = errors.New("not found")

// MemoryStore is an in-memory implementation of consultStore for testing.
type MemoryStore struct {
	mu         sync.RWMutex
	events     map[string]consult.EventSummary
	manifests  []consult.Manifest
}

// NewMemoryStore creates a new in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:    make(map[string]consult.EventSummary),
		manifests: make([]consult.Manifest, 0),
	}
}

// SaveEvent stores an event summary in memory.
func (s *MemoryStore) SaveEvent(ctx context.Context, e consult.EventSummary) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[e.Fingerprint] = e
	s.manifests = append(s.manifests, consult.Manifest{EventID: e.Fingerprint})
	return nil
}

// ListManifests returns manifests with pagination.
func (s *MemoryStore) ListManifests(ctx context.Context, limit, offset int) ([]consult.Manifest, error) {
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
		return []consult.Manifest{}, nil
	}

	end := start + limit
	if end > len(s.manifests) {
		end = len(s.manifests)
	}

	result := make([]consult.Manifest, end-start)
	copy(result, s.manifests[start:end])
	return result, nil
}

// GetEvent retrieves an event summary by fingerprint.
func (s *MemoryStore) GetEvent(ctx context.Context, fingerprint string) (consult.EventSummary, error) {
	select {
	case <-ctx.Done():
		return consult.EventSummary{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.events[fingerprint]
	if !ok {
		return consult.EventSummary{}, ErrNotFound
	}
	return e, nil
}

// StreamManifests returns a channel of all manifests.
func (s *MemoryStore) StreamManifests(ctx context.Context) (<-chan consult.Manifest, <-chan error) {
	ch := make(chan consult.Manifest)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)

		s.mu.RLock()
		manifests := make([]consult.Manifest, len(s.manifests))
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
func (s *MemoryStore) AddTestEvents(events []consult.EventSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range events {
		s.events[e.Fingerprint] = e
		s.manifests = append(s.manifests, consult.Manifest{EventID: e.Fingerprint})
	}
	return nil
}

// Count returns the number of stored events.
func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}

// CreateTestEvent creates a test event summary for use in tests.
func CreateTestEvent(eventID, model, status string) consult.EventSummary {
	return consult.EventSummary{
		SchemaVersion:             consult.EventSchemaVersionV1,
		Fingerprint:               eventID,
		ManifestSerializerVersion: consult.SerializerVersionV1,
		Outcome:                   status,
		Role:                      consult.RoleLeader,
		ByteBudget:                14336,
		ActualBytes:               640,
		TaskSummary:                "test task",
	}
}
