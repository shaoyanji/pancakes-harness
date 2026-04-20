// Package memory implements the three-layer memory architecture for the harness:
//
//   Layer 1 — Lightweight Index (RAM): fast lookup of recent events by fingerprint or timestamp.
//   Layer 2 — Topic Memory Files (disk): consolidated summaries created by the dream daemon.
//   Layer 3 — Full Immutable Event Spine: the existing consult records, never modified, only appended.
//
// The event spine remains the source of truth. Compaction and topic synthesis only affect what
// gets included in the final packet sent downstream — the spine itself is never mutated.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pancakes-harness/internal/eventlog"
)

// IndexEntry is a lightweight RAM index entry for fast event lookup.
type IndexEntry struct {
	EventID       string
	SessionID     string
	BranchID      string
	Kind          string
	Fingerprint   string
	Timestamp     time.Time
	TextPreview   string // first 128 bytes of text content
	BlobRef       string
	Score         float64 // relevance score for compaction/selection
}

// TopicMemory represents a durable topic summary file on disk.
type TopicMemory struct {
	TopicID      string    `json:"topic_id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SourceEvents []string  `json:"source_events"` // event IDs that contributed
	Summary      string    `json:"summary"`
	Tags         []string  `json:"tags,omitempty"`
}

// Config configures the three-layer memory manager.
type Config struct {
	TopicDir       string        // directory for topic memory files (layer 2)
	MaxIndexEntries int          // max entries in the RAM index (layer 1)
	EmbedEnabled    bool          // whether to use local embedder for scoring (optional)
	EmbedFn         EmbedFunc     // optional embedding function for relevance scoring
}

// EmbedFunc computes a relevance score for an event given a query context.
type EmbedFunc func(eventID, kind, textPreview, queryContext string) float64

// Compactor is the interface for context compaction backends.
// Defined here to avoid circular imports with the compactor package.
// Implementations: compactor.GeminiCompactor, compactor.MockCompactor.
type Compactor interface {
	Name() string
}

// Manager coordinates the three memory layers.
type Manager struct {
	cfg Config

	mu     sync.RWMutex
	index  []*IndexEntry
	topics map[string]*TopicMemory

	// Content search index (inverted index + BM25)
	searchIndex *Index

	// cache hit tracking
	cacheHits   int64
	cacheMisses int64
}

// NewManager creates a new three-layer memory manager.
func NewManager(cfg Config) *Manager {
	m := &Manager{
		cfg:         cfg,
		topics:      make(map[string]*TopicMemory),
		searchIndex: NewIndex(),
	}
	if cfg.TopicDir != "" {
		m.loadTopicsFromDisk(cfg.TopicDir)
	}
	return m
}

// --- Layer 1: Lightweight Index (RAM) ---

// IndexEvent adds an event to the lightweight RAM index and the content search index.
func (m *Manager) IndexEvent(ev eventlog.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry := &IndexEntry{
		EventID:   ev.ID,
		SessionID: ev.SessionID,
		BranchID:  ev.BranchID,
		Kind:      ev.Kind,
		Timestamp: ev.TS,
		BlobRef:   ev.BlobRef,
	}
	if fp, ok := ev.Meta["fingerprint"].(string); ok {
		entry.Fingerprint = fp
	}
	if text, ok := ev.Meta["text"].(string); ok {
		if len(text) > 128 {
			entry.TextPreview = text[:128]
		} else {
			entry.TextPreview = text
		}
	}

	m.index = append(m.index, entry)

	// Trim if over capacity (keep most recent)
	maxEntries := m.cfg.MaxIndexEntries
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	if len(m.index) > maxEntries {
		m.index = m.index[len(m.index)-maxEntries:]
	}

	// Content search index (outside the RAM trim — search index is append-only)
	m.searchIndex.Add(ev)
}

// LookupByFingerprint finds recent events matching a fingerprint.
func (m *Manager) LookupByFingerprint(fingerprint string) []IndexEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if fingerprint == "" {
		return nil
	}
	var results []IndexEntry
	for i := len(m.index) - 1; i >= 0; i-- {
		if m.index[i].Fingerprint == fingerprint {
			results = append(results, *m.index[i])
			m.cacheHits++
		}
	}
	if len(results) == 0 {
		m.cacheMisses++
	}
	return results
}

// LookupRecent returns the N most recent events, optionally filtered by kind prefix.
func (m *Manager) LookupRecent(n int, kindPrefix string) []IndexEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []IndexEntry
	for i := len(m.index) - 1; i >= 0 && len(results) < n; i-- {
		e := m.index[i]
		if kindPrefix != "" && !strings.HasPrefix(e.Kind, kindPrefix) {
			continue
		}
		results = append(results, *e)
	}
	// Results are collected in reverse (newest-first); reverse back to chronological.
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

// ScoreEvents applies the optional embedder to score events against a query context.
func (m *Manager) ScoreEvents(eventIDs []string, queryContext string) map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	scores := make(map[string]float64, len(eventIDs))
	if m.cfg.EmbedFn == nil {
		// Default scoring: recency-based (newer = higher score)
		now := time.Now()
		for _, id := range eventIDs {
			for _, e := range m.index {
				if e.EventID == id {
					age := now.Sub(e.Timestamp)
					// Score decays exponentially with age (hours)
					hours := age.Hours()
					if hours < 0 {
						hours = 0
					}
					scores[id] = 1.0 / (1.0 + hours*0.1)
					break
				}
			}
		}
		return scores
	}

	for _, id := range eventIDs {
		for _, e := range m.index {
			if e.EventID == id {
				scores[id] = m.cfg.EmbedFn(e.EventID, e.Kind, e.TextPreview, queryContext)
				break
			}
		}
	}
	return scores
}

// CacheHitRate returns the ratio of cache hits to total lookups.
func (m *Manager) CacheHitRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.cacheHits + m.cacheMisses
	if total == 0 {
		return 0
	}
	return float64(m.cacheHits) / float64(total)
}

// IndexStats returns statistics about the RAM index.
func (m *Manager) IndexStats() IndexStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return IndexStats{
		TotalEntries: len(m.index),
		CacheHits:    m.cacheHits,
		CacheMisses:  m.cacheMisses,
		HitRate:      m.cacheHitRateLocked(),
	}
}

func (m *Manager) cacheHitRateLocked() float64 {
	total := m.cacheHits + m.cacheMisses
	if total == 0 {
		return 0
	}
	return float64(m.cacheHits) / float64(total)
}

type IndexStats struct {
	TotalEntries int     `json:"total_entries"`
	CacheHits    int64   `json:"cache_hits"`
	CacheMisses  int64   `json:"cache_misses"`
	HitRate      float64 `json:"hit_rate"`
}

// --- Layer 2: Topic Memory Files (disk) ---

// CreateTopic creates a new topic memory file.
func (m *Manager) CreateTopic(topic TopicMemory) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if topic.TopicID == "" {
		return fmt.Errorf("topic ID is required")
	}
	if topic.Title == "" {
		return fmt.Errorf("topic title is required")
	}
	if topic.CreatedAt.IsZero() {
		topic.CreatedAt = time.Now().UTC()
	}
	if topic.UpdatedAt.IsZero() {
		topic.UpdatedAt = topic.CreatedAt
	}

	m.topics[topic.TopicID] = &topic

	if m.cfg.TopicDir != "" {
		return m.writeTopicToDisk(topic)
	}
	return nil
}

// GetTopic retrieves a topic memory entry.
func (m *Manager) GetTopic(topicID string) (TopicMemory, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.topics[topicID]
	if !ok {
		return TopicMemory{}, false
	}
	return *t, true
}

// ListTopics returns all known topic memories.
func (m *Manager) ListTopics() []TopicMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]TopicMemory, 0, len(m.topics))
	for _, t := range m.topics {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// UpdateTopic merges new content into an existing topic.
func (m *Manager) UpdateTopic(topicID string, summary string, sourceEvents []string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.topics[topicID]
	if !ok {
		return fmt.Errorf("topic %q not found", topicID)
	}
	t.Summary = summary
	t.SourceEvents = append(t.SourceEvents, sourceEvents...)
	if len(tags) > 0 {
		t.Tags = append(t.Tags, tags...)
		t.Tags = deduplicateStrings(t.Tags)
	}
	t.UpdatedAt = time.Now().UTC()

	if m.cfg.TopicDir != "" {
		return m.writeTopicToDisk(*t)
	}
	return nil
}

// DeleteTopic removes a topic memory entry.
func (m *Manager) DeleteTopic(topicID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.topics, topicID)
}

func (m *Manager) writeTopicToDisk(t TopicMemory) error {
	if m.cfg.TopicDir == "" {
		return nil
	}
	path := filepath.Join(m.cfg.TopicDir, t.TopicID+".json")
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal topic %q: %w", t.TopicID, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write topic %q: %w", t.TopicID, err)
	}
	return os.Rename(tmp, path)
}

func (m *Manager) loadTopicsFromDisk(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		return // silently ignore errors on load
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t TopicMemory
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		if t.TopicID != "" {
			m.topics[t.TopicID] = &t
		}
	}
}

// --- Layer 3: Event Spine (reference only — never mutated) ---

// BuildCompactView creates a compacted representation of events for inclusion in a packet.
// This does NOT modify the event spine — it only selects and scores events.
func (m *Manager) BuildCompactView(events []eventlog.Event, budgetBytes int, queryContext string) ([]eventlog.Event, int, int) {
	if len(events) == 0 {
		return nil, 0, 0
	}

	// Score all events
	eventIDs := make([]string, 0, len(events))
	for _, e := range events {
		eventIDs = append(eventIDs, e.ID)
	}
	scores := m.ScoreEvents(eventIDs, queryContext)

	// Sort by score (highest first), with recency as tiebreaker
	type scoredEvent struct {
		event eventlog.Event
		score float64
	}
	scored := make([]scoredEvent, 0, len(events))
	for _, e := range events {
		s := scores[e.ID]
		scored = append(scored, scoredEvent{event: e, score: s})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].event.TS.After(scored[j].event.TS)
	})

	// Select events that fit within budget
	var selected []eventlog.Event
	var totalBytes int
	for _, se := range scored {
		// Rough byte estimate: event ID + kind + text preview
		estBytes := len(se.event.ID) + len(se.event.Kind) + len(se.event.Meta) * 64
		if totalBytes+estBytes > budgetBytes {
			break
		}
		selected = append(selected, se.event)
		totalBytes += estBytes
	}

	// Sort selected back to chronological order
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].TS.Before(selected[j].TS)
	})

	return selected, len(selected), len(events) - len(selected)
}

// Fork creates a durable topic-memory branch from a set of events.
// This leverages the existing pointer-based branching pattern.
func (m *Manager) Fork(topicID string, title string, sourceEvents []eventlog.Event, summary string) error {
	tags := make([]string, 0)
	eventIDs := make([]string, 0, len(sourceEvents))
	for _, e := range sourceEvents {
		eventIDs = append(eventIDs, e.ID)
	}

	return m.CreateTopic(TopicMemory{
		TopicID:      topicID,
		Title:        title,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		SourceEvents: eventIDs,
		Summary:      summary,
		Tags:         tags,
	})
}

// --- Content Search ---

// SearchEvents searches the event spine by query text using BM25 scoring.
// Returns event IDs and scores, optionally filtered by branch.
func (m *Manager) SearchEvents(query string, branchID string, limit int) []Result {
	return m.searchIndex.Search(query, SearchOpts{
		BranchID: branchID,
		Limit:    limit,
	})
}

// BootstrapSearchIndex indexes a batch of existing events into the search index.
// Call this once at session start with events loaded from the backend.
func (m *Manager) BootstrapSearchIndex(events []eventlog.Event) {
	for _, ev := range events {
		m.searchIndex.Add(ev)
	}
}

// SearchIndexSize returns the number of events in the content search index.
func (m *Manager) SearchIndexSize() int {
	return m.searchIndex.Size()
}

func deduplicateStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
