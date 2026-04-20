// Package preprocess — daemon-sidecar for fast model extraction.
//
// The Daemon is a background event loop that receives messages to preprocess
// via a channel, calls the fast model concurrently, validates against the
// golden schema, and emits results. It is:
//
//   - Fail-fast: hard timeout, errors don't block the caller
//   - Concurrent: multiple extractions can be in-flight
//   - Stackable: results accumulate for debugging/inspection
//   - Decoupled: runs as a sidecar, not a function call in the session
//
// The session submits jobs to the daemon and moves on. The daemon processes
// them asynchronously and records results on the event spine.
package preprocess

import (
	"context"
	"fmt"
	"sync"
	"time"

	"pancakes-harness/internal/eventlog"
)

// FastAdapter is the interface for the fast model. Deliberately minimal —
// the daemon only needs to send text and receive bytes. Parsing/validation
// happens after the call.
type FastAdapter interface {
	Name() string
	Call(ctx context.Context, prompt string, text string) ([]byte, error)
}

// EventLog is the interface for recording results on the event spine.
type EventLog interface {
	AppendEvent(ctx context.Context, e eventlog.Event) error
}

// Config configures the preprocessing daemon.
type Config struct {
	// FastAdapter is the fast model adapter (e.g. Groq gpt-oss-20b).
	FastAdapter FastAdapter
	// EventLog is the spine writer.
	EventLog EventLog
	// Timeout per extraction call. Hard cap — if the fast model doesn't
	// respond within this window, the job is dropped.
	Timeout time.Duration
	// MaxWorkers is the number of concurrent extraction goroutines.
	MaxWorkers int
	// QueueSize is the depth of the job channel.
	QueueSize int
	// OnResult is an optional callback fired on each completed extraction.
	// The session can use this to index in memory or trigger routing.
	OnResult func(result Result)
}

// Job is a unit of work submitted to the daemon.
type Job struct {
	ID        string // event ID or caller-provided identifier
	SessionID string
	BranchID  string
	Text      string // raw message text to extract from
	TS        time.Time
}

// Result is the output of a single extraction pass.
type Result struct {
	JobID       string
	Extraction  *Extraction
	LatencyMs   int
	Error       error // nil on success
	Timestamp   time.Time
}

// Succeeded returns true if the extraction completed without error.
func (r Result) Succeeded() bool {
	return r.Error == nil && r.Extraction != nil
}

// Daemon is the background preprocessing event loop.
type Daemon struct {
	cfg    Config
	queue  chan Job
	done   chan struct{}
	wg     sync.WaitGroup

	mu         sync.Mutex
	running    bool
	results    []Result   // accumulated results for inspection
	totalJobs  int64
	totalOK    int64
	totalErr   int64
	totalMs    int64
}

// NewDaemon creates a new preprocessing daemon.
func NewDaemon(cfg Config) (*Daemon, error) {
	if cfg.FastAdapter == nil {
		return nil, fmt.Errorf("preprocess: FastAdapter is required")
	}
	if cfg.EventLog == nil {
		return nil, fmt.Errorf("preprocess: EventLog is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}

	return &Daemon{
		cfg:   cfg,
		queue: make(chan Job, cfg.QueueSize),
		done:  make(chan struct{}),
	}, nil
}

// Start launches the daemon workers. Non-blocking.
func (d *Daemon) Start(ctx context.Context) {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()

	for i := 0; i < d.cfg.MaxWorkers; i++ {
		d.wg.Add(1)
		go d.worker(ctx, i)
	}
}

// Stop drains the queue and shuts down workers. Blocks until done.
func (d *Daemon) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	d.mu.Unlock()

	close(d.queue)
	d.wg.Wait()
	close(d.done)
}

// Submit enqueues a job for preprocessing. Non-blocking — returns false
// if the queue is full (caller should treat this as "skip preprocessing").
func (d *Daemon) Submit(job Job) bool {
	d.mu.Lock()
	running := d.running
	d.mu.Unlock()
	if !running {
		return false
	}

	select {
	case d.queue <- job:
		return true
	default:
		return false
	}
}

// Results returns a copy of all accumulated results. Useful for debugging
// and inspection — the daemon stacks results so callers can review what
// the fast model extracted across a session.
func (d *Daemon) Results() []Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Result, len(d.results))
	copy(out, d.results)
	return out
}

// Stats returns cumulative daemon statistics.
func (d *Daemon) Stats() DaemonStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	avgMs := int64(0)
	if d.totalOK > 0 {
		avgMs = d.totalMs / d.totalOK
	}
	return DaemonStats{
		TotalJobs:    d.totalJobs,
		SuccessCount: d.totalOK,
		ErrorCount:   d.totalErr,
		AvgLatencyMs: avgMs,
		QueueLen:     len(d.queue),
		QueueCap:     cap(d.queue),
	}
}

// DaemonStats holds cumulative daemon statistics.
type DaemonStats struct {
	TotalJobs    int64 `json:"total_jobs"`
	SuccessCount int64 `json:"success_count"`
	ErrorCount   int64 `json:"error_count"`
	AvgLatencyMs int64 `json:"avg_latency_ms"`
	QueueLen     int   `json:"queue_len"`
	QueueCap     int   `json:"queue_cap"`
}

// worker is the main event loop goroutine.
func (d *Daemon) worker(ctx context.Context, id int) {
	defer d.wg.Done()
	for job := range d.queue {
		result := d.processJob(ctx, job)

		d.mu.Lock()
		d.results = append(d.results, result)
		d.totalJobs++
		if result.Succeeded() {
			d.totalOK++
			d.totalMs += int64(result.LatencyMs)
		} else {
			d.totalErr++
		}
		d.mu.Unlock()

		// Record on spine
		if result.Succeeded() {
			ev := eventlog.Event{
				ID:        job.ID + ".extract",
				SessionID: job.SessionID,
				TS:        result.Timestamp,
				Kind:      eventlog.KindPreprocessExtract,
				BranchID:  job.BranchID,
				Meta:      result.Extraction.Meta(),
			}
			if d.cfg.EventLog != nil {
				_ = d.cfg.EventLog.AppendEvent(ctx, ev)
			}
		}

		// Fire callback
		if d.cfg.OnResult != nil {
			d.cfg.OnResult(result)
		}
	}
}

// processJob runs a single extraction with timeout.
func (d *Daemon) processJob(ctx context.Context, job Job) Result {
	start := time.Now()

	callCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()

	raw, err := d.cfg.FastAdapter.Call(callCtx, extractionPrompt(), job.Text)
	latency := time.Since(start)

	if err != nil {
		return Result{
			JobID:     job.ID,
			Error:     fmt.Errorf("fast model call failed: %w", err),
			LatencyMs: int(latency.Milliseconds()),
			Timestamp: time.Now().UTC(),
		}
	}

	extraction, err := parseExtraction(raw)
	if err != nil {
		return Result{
			JobID:     job.ID,
			Error:     fmt.Errorf("parse extraction: %w", err),
			LatencyMs: int(latency.Milliseconds()),
			Timestamp: time.Now().UTC(),
		}
	}

	if err := extraction.Validate(); err != nil {
		return Result{
			JobID:     job.ID,
			Error:     fmt.Errorf("validation: %w", err),
			LatencyMs: int(latency.Milliseconds()),
			Timestamp: time.Now().UTC(),
		}
	}

	return Result{
		JobID:      job.ID,
		Extraction: extraction,
		LatencyMs:  int(latency.Milliseconds()),
		Timestamp:  time.Now().UTC(),
	}
}
