package memory

import (
	"sort"
	"strings"
	"time"

	"pancakes-harness/internal/eventlog"
)

// CompactionConfig controls how context compaction is triggered and performed.
type CompactionConfig struct {
	// TriggerTurns fires compaction every N turns.
	TriggerTurns int
	// BudgetPressureRatio triggers compaction when budget usage exceeds this ratio (0.0–1.0).
	BudgetPressureRatio float64
	// KeepRecent ensures the most recent N events are never compacted away.
	KeepRecent int
	// MaxCompactEvents limits how many events can be compacted in one pass.
	MaxCompactEvents int
}

// DefaultCompactionConfig provides sensible defaults.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		TriggerTurns:        10,
		BudgetPressureRatio: 0.8,
		KeepRecent:          5,
		MaxCompactEvents:    20,
	}
}

// CompactionResult describes the outcome of a compaction pass.
type CompactionResult struct {
	OriginalCount int
	CompactedCount int
	RemovedCount   int
	BytesSaved     int
	Compacted      bool
}

// CompactByScore compacts a list of events by removing the lowest-scored items,
// while preserving the most recent N events (KeepRecent).
func CompactByScore(events []eventlog.Event, scores map[string]float64, cfg CompactionConfig) (CompactionResult, []eventlog.Event) {
	if len(events) <= cfg.KeepRecent {
		return CompactionResult{OriginalCount: len(events), CompactedCount: len(events)}, events
	}

	// Identify the recent events that must be kept
	keepFrom := len(events) - cfg.KeepRecent
	if keepFrom < 0 {
		keepFrom = 0
	}
	mustKeep := make(map[string]bool)
	for i := keepFrom; i < len(events); i++ {
		mustKeep[events[i].ID] = true
	}

	// Score the remaining events (lower score = more likely to be removed)
	type scored struct {
		event eventlog.Event
		score float64
	}
	var candidates []scored
	for i := 0; i < keepFrom; i++ {
		candidates = append(candidates, scored{
			event: events[i],
			score: scores[events[i].ID],
		})
	}

	// Sort by score ascending (lowest first — these get removed)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		return candidates[i].event.TS.Before(candidates[j].event.TS)
	})

	// Remove lowest-scored candidates, up to MaxCompactEvents
	toRemove := len(candidates)
	if toRemove > cfg.MaxCompactEvents {
		toRemove = cfg.MaxCompactEvents
	}
	// Never remove all pre-recent events; keep at least some context
	if toRemove >= len(candidates) {
		toRemove = len(candidates) / 2
	}

	var kept []eventlog.Event
	for i := 0; i < len(candidates)-toRemove; i++ {
		kept = append(kept, candidates[i].event)
	}
	// Append the must-keep recent events
	for i := keepFrom; i < len(events); i++ {
		kept = append(kept, events[i])
	}

	// Sort back to chronological order
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].TS.Before(kept[j].TS)
	})

	return CompactionResult{
		OriginalCount:  len(events),
		CompactedCount: len(kept),
		RemovedCount:   toRemove,
		Compacted:      toRemove > 0,
	}, kept
}

// CompactByTextSize compacts events by estimating text size and removing
// the largest, least-relevant items first.
func CompactByTextSize(events []eventlog.Event, budgetBytes int, cfg CompactionConfig) (CompactionResult, []eventlog.Event) {
	if len(events) <= cfg.KeepRecent {
		return CompactionResult{OriginalCount: len(events), CompactedCount: len(events)}, events
	}

	// Estimate total text size
	type sized struct {
		event    eventlog.Event
		textSize int
	}
	var items []sized
	for _, e := range events {
		ts := estimateEventTextSize(e)
		items = append(items, sized{event: e, textSize: ts})
	}

	// Calculate total size
	totalSize := 0
	for _, it := range items {
		totalSize += it.textSize
	}

	if totalSize <= budgetBytes {
		return CompactionResult{OriginalCount: len(events), CompactedCount: len(events), BytesSaved: 0}, events
	}

	// Identify recent events to keep
	keepFrom := len(items) - cfg.KeepRecent
	if keepFrom < 0 {
		keepFrom = 0
	}

	// Sort candidates by text size descending (largest first to remove)
	candidates := items[:keepFrom]
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].textSize > candidates[j].textSize
	})

	// Remove largest items until we fit within budget
	removed := 0
	currentSize := totalSize
	for i := 0; i < len(candidates) && removed < cfg.MaxCompactEvents && currentSize > budgetBytes; i++ {
		currentSize -= candidates[i].textSize
		removed++
	}

	// Reconstruct kept events
	var kept []eventlog.Event
	for i := removed; i < len(candidates); i++ {
		kept = append(kept, candidates[i].event)
	}
	for i := keepFrom; i < len(items); i++ {
		kept = append(kept, items[i].event)
	}

	sort.Slice(kept, func(i, j int) bool {
		return kept[i].TS.Before(kept[j].TS)
	})

	return CompactionResult{
		OriginalCount:  len(events),
		CompactedCount: len(kept),
		RemovedCount:   removed,
		BytesSaved:     totalSize - currentSize,
		Compacted:      removed > 0,
	}, kept
}

func estimateEventTextSize(e eventlog.Event) int {
	if e.Meta == nil {
		return 0
	}
	if text, ok := e.Meta["text"].(string); ok {
		return len(text)
	}
	if summary, ok := e.Meta["summary"].(string); ok {
		return len(summary)
	}
	if reason, ok := e.Meta["reason"].(string); ok {
		return len(reason)
	}
	// Estimate based on meta keys
	total := 0
	for k, v := range e.Meta {
		s := strings.TrimSpace(k)
		if s == "text" || s == "summary" {
			continue
		}
		total += len(s) + len(stringifyMeta(v))
	}
	return total
}

func stringifyMeta(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case nil:
		return ""
	default:
		return ""
	}
}

// ShouldCompact determines whether compaction should be triggered based on
// turn count and/or budget pressure.
func ShouldCompact(turnsSinceLastCompact int, budgetUsageRatio float64, cfg CompactionConfig) bool {
	if cfg.TriggerTurns > 0 && turnsSinceLastCompact >= cfg.TriggerTurns {
		return true
	}
	if cfg.BudgetPressureRatio > 0 && budgetUsageRatio >= cfg.BudgetPressureRatio {
		return true
	}
	return false
}

// CompactAgeThreshold returns events older than the given duration, useful
// for time-based compaction policies.
func CompactAgeThreshold(events []eventlog.Event, maxAge time.Duration, cfg CompactionConfig) (CompactionResult, []eventlog.Event) {
	if len(events) <= cfg.KeepRecent {
		return CompactionResult{OriginalCount: len(events), CompactedCount: len(events)}, events
	}

	now := time.Now()
	cutoff := now.Add(-maxAge)

	keepFrom := len(events) - cfg.KeepRecent
	if keepFrom < 0 {
		keepFrom = 0
	}

	var kept []eventlog.Event
	removed := 0
	for i := 0; i < keepFrom && removed < cfg.MaxCompactEvents; i++ {
		if events[i].TS.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, events[i])
	}
	for i := keepFrom; i < len(events); i++ {
		kept = append(kept, events[i])
	}

	sort.Slice(kept, func(i, j int) bool {
		return kept[i].TS.Before(kept[j].TS)
	})

	return CompactionResult{
		OriginalCount:  len(events),
		CompactedCount: len(kept),
		RemovedCount:   removed,
		Compacted:      removed > 0,
	}, kept
}
