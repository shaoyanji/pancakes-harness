// Package main is the entrypoint for the dream daemon standalone binary.
//
// The dream daemon runs as a background job that performs reflective passes
// over memory files after periods of inactivity.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/dream"
	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/memory"
)

const version = "0.3.1"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("dream-daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	showVersion := fs.Bool("version", false, "show version")
	showHelp := fs.Bool("h", false, "show help")
	sessionID := fs.String("session-id", stringOrDefault(getenv("HARNESS_SESSION_ID"), "demo"), "session ID")
	topicDir := fs.String("topic-dir", stringOrDefault(getenv("DREAM_TOPIC_DIR"), ""), "directory for topic memory files")
	inactivityHrs := fs.Int("inactivity-hrs", parseIntOrDefault(getenv("DREAM_INACTIVITY_HOURS"), 24), "hours of inactivity before triggering dream")
	minSessions := fs.Int("min-sessions", parseIntOrDefault(getenv("DREAM_MIN_SESSIONS"), 5), "minimum sessions before triggering dream")
	force := fs.Bool("force", false, "force execute dream pass regardless of thresholds")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintf(stdout, "dream-daemon %s\n", version)
		return 0
	}
	if *showHelp {
		fmt.Fprint(stdout, usage())
		return 0
	}

	// Initialize components
	memMgr := memory.NewManager(memory.Config{
		TopicDir:       *topicDir,
		MaxIndexEntries: 1024,
	})

	// Use memory backend as event log
	b := backend.NewMemoryBackend()
	daemon := dream.NewDaemon(dream.Config{
		Enabled:         true,
		InactivityHours: *inactivityHrs,
		MinSessions:     *minSessions,
		TopicDir:        *topicDir,
	}, memMgr, backendEventLog{b})

	ctx := context.Background()

	if *force {
		fmt.Fprintf(stdout, "Executing forced dream pass for session %s...\n", *sessionID)
		result, err := daemon.Execute(ctx, *sessionID)
		if err != nil {
			fmt.Fprintf(stderr, "dream execution failed: %v\n", err)
			return 1
		}
		printDreamResult(stdout, result)
		return 0
	}

	// Check thresholds
	daemon.RecordActivity() // simulate activity for threshold check
	if !daemon.ShouldDream() {
		fmt.Fprintf(stdout, "Dream thresholds not met (inactivity: %dh, min sessions: %d).\n", *inactivityHrs, *minSessions)
		fmt.Fprintf(stdout, "Use --force to execute a dream pass regardless.\n")
		return 0
	}

	fmt.Fprintf(stdout, "Executing dream pass for session %s...\n", *sessionID)
	result, err := daemon.Execute(ctx, *sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "dream execution failed: %v\n", err)
		return 1
	}
	printDreamResult(stdout, result)
	return 0
}

func printDreamResult(w io.Writer, result *dream.DreamResult) {
	fmt.Fprintf(w, "Dream pass completed in %s:\n", result.Duration)
	fmt.Fprintf(w, "  Events reviewed: %d\n", result.EventsReviewed)
	fmt.Fprintf(w, "  Topics created: %d\n", len(result.TopicsCreated))
	fmt.Fprintf(w, "  Topics updated: %d\n", len(result.TopicsUpdated))
	fmt.Fprintf(w, "  Topics pruned: %d\n", len(result.TopicsPruned))
	if result.Summary != "" {
		summary := result.Summary
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
		fmt.Fprintf(w, "\nSummary:\n%s\n", summary)
	}
}

func stringOrDefault(v, d string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return d
	}
	return v
}

func parseIntOrDefault(raw string, d int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return d
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return d
	}
	return n
}

func usage() string {
	return `dream-daemon 0.3.1

Background "sleep-time dreaming" daemon for pancakes-harness.
Performs reflective passes over memory files after periods of inactivity.

Usage:
  dream-daemon [flags]

Flags:
  -session-id string
        session ID (default: demo)
  -topic-dir string
        directory for topic memory files
  -inactivity-hrs int
        hours of inactivity before triggering dream (default: 24)
  -min-sessions int
        minimum sessions before triggering dream (default: 5)
  -force
        force execute dream pass regardless of thresholds
  -version
        show version
  -h
        show help

Example:
  dream-daemon -session-id demo -topic-dir /tmp/topics -force
`
}

// backendEventLog adapts backend.Backend to dream.EventLog
type backendEventLog struct {
	b *backend.MemoryBackend
}

func (w backendEventLog) ListBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error) {
	return w.b.ListEventsBySession(ctx, sessionID)
}

func (w backendEventLog) AppendEvent(ctx context.Context, e eventlog.Event) error {
	return w.b.AppendEvent(ctx, e)
}
