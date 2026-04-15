// Package dream implements the background "sleep-time dreaming" daemon.
//
// Inspired by KAIROS, the dream daemon runs after >= 24h of inactivity and >= 5 completed sessions.
// It performs a reflective pass over memory files, synthesizing durable topic memories.
//
// The daemon is optional and configurable via environment variables:
//   DREAM_ENABLED=true
//   DREAM_INACTIVITY_HOURS=24
//   DREAM_MIN_SESSIONS=5
package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/memory"
)

// DefaultDreamPrompt is the reflective sub-agent prompt used during a dream pass.
const DefaultDreamPrompt = `You are performing a dream, a reflective pass over your memory files.
Synthesize what you have learned recently into durable, well-organized memories
so that future sessions can orient quickly.
Prune contradictions, merge duplicates, rewrite topic files.
Focus on patterns, lessons, and durable knowledge.`

// Config configures the dream daemon.
type Config struct {
	// Enabled controls whether the daemon is active.
	Enabled bool
	// InactivityHours is the minimum idle time before dreaming triggers.
	InactivityHours int
	// MinSessions is the minimum number of completed sessions before dreaming triggers.
	MinSessions int
	// DreamPrompt overrides the default reflective prompt.
	DreamPrompt string
	// TopicDir is where topic memory files are stored (layer 2).
	TopicDir string
}

// Daemon is the dream daemon background worker.
type Daemon struct {
	cfg       Config
	memoryMgr *memory.Manager
	eventLog  EventLog

	mu              sync.Mutex
	lastDreamTime   time.Time
	sessionCount    int
	lastActivityTS  time.Time
	dreamCount      int64
}

// EventLog is the interface for reading events and appending dream results.
type EventLog interface {
	ListBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error)
	AppendEvent(ctx context.Context, e eventlog.Event) error
}

// DreamResult captures the output of a dream pass.
type DreamResult struct {
	TopicsCreated  []string
	TopicsUpdated    []string
	TopicsPruned     []string
	Summary          string
	Timestamp        time.Time
	Duration         time.Duration
	SessionCount     int
	EventsReviewed   int
	NewTopics        map[string]string // topicID -> summary
}

// NewDaemon creates a new dream daemon.
func NewDaemon(cfg Config, memoryMgr *memory.Manager, eventLog EventLog) *Daemon {
	if cfg.InactivityHours <= 0 {
		cfg.InactivityHours = 24
	}
	if cfg.MinSessions <= 0 {
		cfg.MinSessions = 5
	}
	if cfg.DreamPrompt == "" {
		cfg.DreamPrompt = DefaultDreamPrompt
	}
	return &Daemon{
		cfg:       cfg,
		memoryMgr: memoryMgr,
		eventLog:  eventLog,
	}
}

// RecordActivity updates the last-activity timestamp and session count.
func (d *Daemon) RecordActivity() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastActivityTS = time.Now().UTC()
	d.sessionCount++
}

// ShouldDream checks whether the daemon should trigger based on inactivity and session thresholds.
func (d *Daemon) ShouldDream() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.cfg.Enabled {
		return false
	}
	if d.lastActivityTS.IsZero() {
		return false
	}
	inactivity := time.Since(d.lastActivityTS)
	if inactivity.Hours() < float64(d.cfg.InactivityHours) {
		return false
	}
	if d.sessionCount < d.cfg.MinSessions {
		return false
	}
	// Cooldown: don't dream more than once per inactivity period
	if !d.lastDreamTime.IsZero() && time.Since(d.lastDreamTime).Hours() < float64(d.cfg.InactivityHours) {
		return false
	}
	return true
}

// Execute runs a dream pass. This is the core reflective sub-agent.
//
// In production, this would call a model with the dream prompt and the current memory files.
// For now, it performs a local synthesis based on the event spine and existing topics.
func (d *Daemon) Execute(ctx context.Context, sessionID string) (*DreamResult, error) {
	started := time.Now()

	events, err := d.eventLog.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}

	// Gather topic summaries for the dream context
	topics := d.memoryMgr.ListTopics()

	// Perform the reflective synthesis
	result := d.reflectivePass(events, topics)
	result.Timestamp = started
	result.Duration = time.Since(started)
	result.SessionCount = d.sessionCount
	result.EventsReviewed = len(events)

	// Create/update topic memories from dream synthesis
	for topicID, summary := range result.NewTopics {
		existing, ok := d.memoryMgr.GetTopic(topicID)
		if ok {
			if err := d.memoryMgr.UpdateTopic(topicID, summary, nil, nil); err != nil {
				result.TopicsUpdated = append(result.TopicsUpdated, topicID)
			} else {
				_ = existing
				result.TopicsUpdated = append(result.TopicsUpdated, topicID)
			}
		} else {
			if err := d.memoryMgr.CreateTopic(memory.TopicMemory{
				TopicID:   topicID,
				Title:     topicTitleFromID(topicID),
				Summary:   summary,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				return nil, fmt.Errorf("create topic %q: %w", topicID, err)
			}
			result.TopicsCreated = append(result.TopicsCreated, topicID)
		}
	}

	// Record the dream result as a special consult event on the spine
	if err := d.appendDreamEvent(ctx, sessionID, result); err != nil {
		// Non-fatal: the dream synthesis still succeeded
	}

	d.mu.Lock()
	d.lastDreamTime = time.Now().UTC()
	d.dreamCount++
	d.mu.Unlock()

	return result, nil
}

// DreamCount returns the total number of dream passes executed.
func (d *Daemon) DreamCount() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dreamCount
}

// DreamFrequency returns the approximate frequency of dream passes (passes per day).
func (d *Daemon) DreamFrequency() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.lastDreamTime.IsZero() || d.dreamCount < 2 {
		return 0
	}
	// Estimate based on count and time since first dream
	return 0 // Simplified: would need start time tracking for accurate calculation
}

func (d *Daemon) reflectivePass(events []eventlog.Event, topics []memory.TopicMemory) *DreamResult {
	result := &DreamResult{
		NewTopics: make(map[string]string),
	}

	// Analyze event patterns to identify topics
	topicCandidates := d.identifyTopics(events)

	// Synthesize summaries for each topic
	for topicID, topicEvents := range topicCandidates {
		summary := d.synthesizeSummary(topicEvents)
		if summary == "" {
			continue
		}
		result.NewTopics[topicID] = summary
	}

	// Review existing topics for contradictions and merges
	result = d.pruneAndMerge(result, topics)

	// Build overall summary
	var summaries []string
	for _, summary := range result.NewTopics {
		summaries = append(summaries, summary)
	}
	result.Summary = strings.Join(summaries, "\n\n---\n\n")
	if result.Summary == "" {
		result.Summary = "No new patterns identified during this dream pass."
	}

	return result
}

func (d *Daemon) identifyTopics(events []eventlog.Event) map[string][]eventlog.Event {
	candidates := make(map[string][]eventlog.Event)

	// Group events by kind prefix and meta patterns
	for _, e := range events {
		// Use kind prefix as basic topic grouping
		parts := strings.SplitN(e.Kind, ".", 2)
		if len(parts) > 1 {
			prefix := parts[0]
			candidates[prefix] = append(candidates[prefix], e)
		}

		// Look for task_summary patterns to identify themes
		if task, ok := e.Meta["task_summary"].(string); ok && task != "" {
			// Extract first word as rough topic
			words := strings.Fields(task)
			if len(words) > 0 {
				topic := "task_" + strings.ToLower(words[0])
				candidates[topic] = append(candidates[topic], e)
			}
		}
	}

	// Filter out topics with too few events
	filtered := make(map[string][]eventlog.Event)
	for topic, evts := range candidates {
		if len(evts) >= 2 {
			filtered[topic] = evts
		}
	}
	return filtered
}

func (d *Daemon) synthesizeSummary(events []eventlog.Event) string {
	if len(events) == 0 {
		return ""
	}

	// Count kinds
	kindCounts := make(map[string]int)
	for _, e := range events {
		kindCounts[e.Kind]++
	}

	// Build summary
	var lines []string
	lines = append(lines, fmt.Sprintf("Pattern analysis of %d events:", len(events)))

	// Top kinds
	type kindCount struct {
		kind  string
		count int
	}
	var kcList []kindCount
	for k, c := range kindCounts {
		kcList = append(kcList, kindCount{kind: k, count: c})
	}
	sort.Slice(kcList, func(i, j int) bool {
		return kcList[i].count > kcList[j].count
	})

	for _, kc := range kcList[:minInt(len(kcList), 5)] {
		lines = append(lines, fmt.Sprintf("  - %s: %d occurrences", kc.kind, kc.count))
	}

	// Time span
	if len(events) >= 2 {
		first := events[0].TS
		last := events[len(events)-1].TS
		lines = append(lines, fmt.Sprintf("  Time span: %s to %s", first.Format(time.RFC3339), last.Format(time.RFC3339)))
	}

	return strings.Join(lines, "\n")
}

func (d *Daemon) pruneAndMerge(result *DreamResult, existingTopics []memory.TopicMemory) *DreamResult {
	// Simple deduplication: check for overlapping topic IDs
	seen := make(map[string]bool)
	for _, t := range existingTopics {
		seen[t.TopicID] = true
	}

	for topicID := range result.NewTopics {
		if seen[topicID] {
			result.TopicsPruned = append(result.TopicsPruned, topicID)
		}
	}

	return result
}

func (d *Daemon) appendDreamEvent(ctx context.Context, sessionID string, result *DreamResult) error {
	if d.eventLog == nil {
		return nil
	}

	topicsCreated, _ := json.Marshal(result.TopicsCreated)
	topicsUpdated, _ := json.Marshal(result.TopicsUpdated)

	ev := eventlog.Event{
		ID:        fmt.Sprintf("dream.%d", result.Timestamp.UnixNano()),
		SessionID: sessionID,
		TS:        result.Timestamp,
		Kind:      eventlog.KindDreamResult,
		BranchID:  "main",
		Meta: map[string]any{
			"topics_created":   string(topicsCreated),
			"topics_updated":   string(topicsUpdated),
			"topics_pruned":    result.TopicsPruned,
			"summary":          result.Summary,
			"duration_ms":      result.Duration.Milliseconds(),
			"session_count":    result.SessionCount,
			"events_reviewed":  result.EventsReviewed,
		},
	}
	return d.eventLog.AppendEvent(ctx, ev)
}

func topicTitleFromID(id string) string {
	parts := strings.SplitN(id, "_", 2)
	if len(parts) == 2 {
		return strings.Title(parts[1]) //nolint:staticcheck // simple title case
	}
	return id
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
