package replay

import (
	"context"
	"errors"

	"pancakes-harness/internal/eventlog"
)

var ErrMixedSessionReplay = errors.New("replay input contains multiple sessions")

type SessionState struct {
	SessionID   string
	BranchHeads map[string]string
	LastEventID string
	EventCount  int
}

func RebuildSession(events []eventlog.Event) (SessionState, error) {
	state := SessionState{
		BranchHeads: make(map[string]string),
	}
	if len(events) == 0 {
		return state, nil
	}

	state.SessionID = events[0].SessionID
	for _, e := range events {
		if e.SessionID != state.SessionID {
			return SessionState{}, ErrMixedSessionReplay
		}
		if err := e.Validate(); err != nil {
			return SessionState{}, err
		}
		state.BranchHeads[e.BranchID] = e.ID
		state.LastEventID = e.ID
		state.EventCount++
	}
	return state, nil
}

func RebuildFromStore(ctx context.Context, store eventlog.Store, sessionID string) (SessionState, error) {
	events, err := store.ListBySession(ctx, sessionID)
	if err != nil {
		return SessionState{}, err
	}
	return RebuildSession(events)
}
