// Package review provides a CLI-only interface to list, inspect, and export consult records.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"pancakes-harness/internal/consult"
)

// ReviewSvc hides the storage backend.
type ReviewSvc struct {
	store consultStore
}

// consultStore is the minimal adapter subset needed for review.
type consultStore interface {
	ListManifests(ctx context.Context, limit, offset int) ([]consult.Manifest, error)
	GetEvent(ctx context.Context, eventID string) (consult.EventSummary, error)
	StreamManifests(ctx context.Context) (<-chan consult.Manifest, <-chan error)
}

// NewReviewSvc creates a new review service with the given store.
func NewReviewSvc(store consultStore) *ReviewSvc {
	return &ReviewSvc{store: store}
}

// ListRecent returns the most recent consult manifests.
func (s *ReviewSvc) ListRecent(ctx context.Context, limit int) ([]consult.Manifest, error) {
	if limit <=0 {
		limit = 20
	}
	return s.store.ListManifests(ctx, limit, 0)
}

// Show returns the full consult event for the given event ID.
func (s *ReviewSvc) Show(ctx context.Context, eventID string) (consult.EventSummary, error) {
	return s.store.GetEvent(ctx, eventID)
}

// Export writes all consults as JSON lines to w.
func (s *ReviewSvc) Export(ctx context.Context, w io.Writer) error {
	ch, errCh := s.store.StreamManifests(ctx)
	for m := range ch {
		e, err := s.store.GetEvent(ctx, m.EventID)
		if err != nil {
			return fmt.Errorf("export: event %s: %w", m.EventID, err)
		}
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("export: marshal %s: %w", m.EventID, err)
		}
		if _, err := fmt.Fprintln(w, string(data)); err != nil {
			return err
		}
	}
	return <-errCh
}
