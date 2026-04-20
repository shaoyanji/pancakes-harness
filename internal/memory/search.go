// Content search over the event spine using an inverted index with BM25 scoring.
//
// The index is built at event ingestion time (append-only, matching the immutable
// event spine). Search returns ranked event IDs that can be marked IsGlobalRelevant
// before egress.Select runs — no selector changes needed.
package memory

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"pancakes-harness/internal/eventlog"
)

// Posting records one event's association with a term.
type Posting struct {
	EventID   string
	TermFreq  int
	FieldBoost float64 // 1.0 = normal text, >1.0 = AST symbol or other boosted field
}

// EventStats stores per-event metadata for BM25 length normalization.
type EventStats struct {
	TokenCount int
	BranchID   string
}

// Index is an inverted index with BM25 scoring over the event spine.
type Index struct {
	mu         sync.RWMutex
	terms      map[string][]Posting
	eventStats map[string]EventStats
	docCount   int
	avgDocLen  float64
}

// NewIndex creates an empty search index.
func NewIndex() *Index {
	return &Index{
		terms:      make(map[string][]Posting),
		eventStats: make(map[string]EventStats),
	}
}

// SearchOpts controls search behavior.
type SearchOpts struct {
	BranchID string // if non-empty, restrict results to this branch
	Limit    int    // max results; 0 = default (20)
}

// Result is a scored event match.
type Result struct {
	EventID string
	Score   float64
}

// Add indexes an event. Plain text tokens are extracted automatically.
// If the event text parses as Go source, AST symbols are added with boost.
func (idx *Index) Add(ev eventlog.Event) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	text := eventText(ev)
	if text == "" {
		return
	}

	// Tokenize plain text
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return
	}

	// Count term frequencies for plain text
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}

	stats := EventStats{
		TokenCount: len(tokens),
		BranchID:   ev.BranchID,
	}

	// Try AST enrichment — extract symbols and add with boost
	symbols, _ := extractSymbols(text)
	symbolTF := make(map[string]int)
	for _, s := range symbols {
		s := strings.ToLower(s)
		symbolTF[s]++
	}

	// Insert plain text postings
	for term, freq := range tf {
		idx.terms[term] = append(idx.terms[term], Posting{
			EventID:    ev.ID,
			TermFreq:   freq,
			FieldBoost: 1.0,
		})
	}

	// Insert AST symbol postings with boost (insert 3x to inflate TF)
	for term, freq := range symbolTF {
		boostedFreq := freq * 3
		idx.terms[term] = append(idx.terms[term], Posting{
			EventID:    ev.ID,
			TermFreq:   boostedFreq,
			FieldBoost: 3.0,
		})
		// Also add the symbol to plain-text stats so length normalization stays honest
		stats.TokenCount += boostedFreq
	}

	idx.eventStats[ev.ID] = stats
	idx.docCount++
	idx.recomputeAvgDocLen()
}

// Search returns ranked events matching the query, scored by BM25.
func (idx *Index) Search(query string, opts SearchOpts) []Result {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}

	// BM25 parameters
	const k1 = 1.2
	const b = 0.75

	// Accumulate scores per event
	scores := make(map[string]float64)
	N := float64(idx.docCount)
	avgdl := idx.avgDocLen

	for _, qt := range queryTerms {
		postings := idx.terms[qt]
		if len(postings) == 0 {
			continue
		}

		// IDF: log((N - df + 0.5) / (df + 0.5) + 1)
		df := float64(len(postings))
		idf := math.Log((N-df+0.5)/(df+0.5) + 1.0)

		for _, p := range postings {
			// Branch filter
			if opts.BranchID != "" {
				stats, ok := idx.eventStats[p.EventID]
				if !ok || stats.BranchID != opts.BranchID {
					continue
				}
			}

			stats := idx.eventStats[p.EventID]
			dl := float64(stats.TokenCount)
			tf := float64(p.TermFreq)

			// BM25 score for this term-event pair
			numerator := tf * (k1 + 1)
			denominator := tf + k1*(1-b+b*(dl/avgdl))
			score := idf * (numerator / denominator)

			scores[p.EventID] += score
		}
	}

	// Sort by score descending
	results := make([]Result, 0, len(scores))
	for id, score := range scores {
		results = append(results, Result{EventID: id, Score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// Size returns the number of indexed events.
func (idx *Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.docCount
}

func (idx *Index) recomputeAvgDocLen() {
	if idx.docCount == 0 {
		idx.avgDocLen = 0
		return
	}
	total := 0
	for _, s := range idx.eventStats {
		total += s.TokenCount
	}
	idx.avgDocLen = float64(total) / float64(idx.docCount)
}

// eventText extracts the primary text content from an event.
func eventText(ev eventlog.Event) string {
	if ev.Meta == nil {
		return ""
	}
	// Prefer "text", fall back to "summary"
	if t, ok := ev.Meta["text"].(string); ok && t != "" {
		return t
	}
	if s, ok := ev.Meta["summary"].(string); ok && s != "" {
		return s
	}
	// Concatenate any string values from meta as fallback
	var parts []string
	for k, v := range ev.Meta {
		if s, ok := v.(string); ok && s != "" && k != "schema_version" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// tokenize splits text into lowercase terms, dropping short tokens.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current.WriteRune(r)
		} else {
			if current.Len() >= 2 {
				tokens = append(tokens, current.String())
			}
			current.Reset()
		}
	}
	if current.Len() >= 2 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
