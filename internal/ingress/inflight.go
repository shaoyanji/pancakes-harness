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

	value any
	err   error
}

// Ticket represents a caller's role in an in-flight request.
// Exactly one ticket per key is leader while the request is active.
type Ticket struct {
	leader  bool
	done    <-chan struct{}
	read    func() (any, error)
	release func(any, error)
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
		return Ticket{
			leader: false,
			done:   c.done,
			read: func() (any, error) {
				return c.value, c.err
			},
		}
	}

	c := &call{done: make(chan struct{})}
	i.calls[key] = c

	return Ticket{
		leader: true,
		done:   c.done,
		read: func() (any, error) {
			return c.value, c.err
		},
		release: func(v any, err error) {
			c.once.Do(func() {
				c.value = v
				c.err = err
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
	t.Complete(nil, nil)
}

// Complete marks the leader request complete and publishes the coalesced result.
// Calling Complete on a follower is a no-op.
func (t Ticket) Complete(value any, err error) {
	if t.release != nil {
		t.release(value, err)
	}
}

// Wait blocks until the leader finishes, or context cancellation.
func (t Ticket) Wait(ctx context.Context) error {
	_, err := t.WaitValue(ctx)
	return err
}

// WaitValue blocks until the leader publishes completion and returns that result.
func (t Ticket) WaitValue(ctx context.Context) (any, error) {
	select {
	case <-t.done:
		if t.read == nil {
			return nil, nil
		}
		return t.read()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
