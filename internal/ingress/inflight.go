package ingress

import (
	"context"
	"sync"
)

// Inflight provides simple in-flight coalescing by request key.
type Inflight struct {
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done chan struct{}
	once sync.Once
}

// Ticket represents a caller's role in an in-flight request.
// Exactly one ticket per key is leader while the request is active.
type Ticket struct {
	leader  bool
	done    <-chan struct{}
	release func()
}

// NewInflight creates an empty in-flight coalescer.
func NewInflight() *Inflight {
	return &Inflight{calls: make(map[string]*call)}
}

// Enter registers a request key and returns a leader/follower ticket.
func (i *Inflight) Enter(key string) Ticket {
	i.mu.Lock()
	defer i.mu.Unlock()

	if c, ok := i.calls[key]; ok {
		return Ticket{leader: false, done: c.done}
	}

	c := &call{done: make(chan struct{})}
	i.calls[key] = c

	return Ticket{
		leader: true,
		done:   c.done,
		release: func() {
			c.once.Do(func() {
				close(c.done)
				i.mu.Lock()
				delete(i.calls, key)
				i.mu.Unlock()
			})
		},
	}
}

// Leader reports whether this caller is the leader for the request key.
func (t Ticket) Leader() bool {
	return t.leader
}

// Done marks the leader request complete and releases followers.
// Calling Done on a follower is a no-op.
func (t Ticket) Done() {
	if t.release != nil {
		t.release()
	}
}

// Wait blocks until the leader finishes, or context cancellation.
func (t Ticket) Wait(ctx context.Context) error {
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
