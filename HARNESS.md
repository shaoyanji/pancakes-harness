# pancakes-harness v0.3.1 — AI Harness Guide

This document describes the v0.3.1 harness with self-healing, context compaction, three-layer memory, dreaming, and self-auditing.

## Architecture Overview

The harness is a **local-first context and egress kernel**. It reconstructs local consult context, shapes bounded model-facing artifacts under a strict 14,336-byte HTTP envelope, persists replayable consult records, and exposes a thin HTTP ingress API. It is intentionally not a full agent control plane.

### Core Invariants (preserved from v0.2.x)
- Local-first state as source of truth
- Hard 14,336-byte packet budget (never exceeded)
- Deterministic packet assembly from the local event spine
- Fully replayable consult events
- Pointer-based branching
- Explainable egress selection
- Thin kernel design

### New v0.3.0 Features

| Feature | Package | Description |
|---------|---------|-------------|
| Self-Healing Loop | `internal/consult/loop.go` | Automatic retry + model fallback on recoverable failures |
| Three-Layer Memory | `internal/memory/` | RAM index, topic files on disk, immutable event spine |
| Context Compaction | `internal/memory/compaction.go` | Score-based trimming under budget pressure |
| Dream Daemon | `internal/dream/` | Background reflective pass over memory files |
| Opinionated Tooling | `internal/tooling/` | Structured, typed tools with parallel reads / serial writes |
| Self-Auditing | `internal/audit/` | Cost-aware termination after every turn |

---

## 1. Self-Healing Query Loop

The self-healing loop wraps turn execution inside a small state machine:

1. **Primary attempt** with the configured model adapter
2. **Automatic retry** (once) with a meta-recovery prompt injected on recoverable failures:
   > "Continue from last stable checkpoint. Do not repeat prior output."
3. **Model fallback** to the next cheaper model in config (if any)
4. **Event spine recording** of every recovery attempt for full replayability

### Recoverable Errors
- Token-budget exhaustion
- Context overflow
- Model timeouts / rate limits
- Service unavailability

### Configuration
The self-healing loop is enabled by default. Recovery attempts are recorded as `recovery.attempt` and `recovery.fallback` events in the consult event spine.

---

## 2. Three-Layer Memory

The memory architecture has three layers:

### Layer 1: Lightweight Index (RAM)
- Fast lookup of recent events by fingerprint or timestamp
- Configurable max entries (default: 1024)
- Tracks cache hit/miss statistics

### Layer 2: Topic Memory Files (Disk)
- Consolidated summaries created by the dream daemon
- Stored as JSON files in the configured topic directory
- Support create, update, merge, and prune operations

### Layer 3: Full Immutable Event Spine
- The existing consult records — never modified, only appended
- Source of truth for all replays and audits

### Compaction Rules
Triggered on budget pressure or every N turns:
- Score messages by recency + relevance heuristic
- Trim lowest-value messages while keeping the hard 14,336-byte cap
- Store the compacted view as a new event type (`context.compact`)

### `memory.Fork` Method
Creates durable topic-memory branches from a set of events:
```go
mgr.Fork("topic_id", "Topic Title", sourceEvents, "summary text")
```

---

## 3. Dream Daemon

The dream daemon runs after ≥24h of inactivity and ≥5 completed sessions (configurable).

### Behavior
- Performs a reflective sub-agent prompt on the current memory files
- Synthesizes durable topic memories from event patterns
- Prunes contradictions, merges duplicates, rewrites topic files
- Logs the dream result as a `dream.result` event on the spine

### Triggering
**Automatic**: Via thresholds (inactivity + session count)
**Manual**: POST to `/v1/dream` with `{"trigger": true}`
**CLI**: `dream-daemon -force`

### Environment Variables
| Variable | Default | Description |
|----------|---------|-------------|
| `DREAM_ENABLED` | `false` | Enable the dream daemon |
| `DREAM_INACTIVITY_HOURS` | `24` | Hours of inactivity before triggering |
| `DREAM_MIN_SESSIONS` | `5` | Minimum sessions before triggering |
| `DREAM_TOPIC_DIR` | `` | Directory for topic memory files |

---

## 4. Opinionated Tooling Layer (Opt-In)

Tools are NOT enabled by default. When configured via `/v1/agent-call`, the egress path enforces:

- Only structured, typed tools (no raw shell)
- Reads may run in parallel; writes must be strictly serial
- Tool list is always sorted alphabetically (KV-cache optimization)

### Built-in Safe Tools (examples)
| Tool | Type | Description |
|------|------|-------------|
| `grep` | read | Search for a pattern in text |
| `glob` | read | List files matching a pattern |
| `read` | read | Read content by reference |
| `write` | write | Write content by reference |

---

## 5. Self-Auditing & Cost-Aware Termination

After every turn, a lightweight self-audit determines whether to continue or terminate early.

### Audit Prompt
> "Do I have enough information to answer the user query, or should I continue?"

### Behavior
- Tracks cumulative tokens/cost per consult
- Supports early termination when audit says "complete" or budget threshold is hit
- Records audit decision as `audit.decision` event in the spine

### Configuration
| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_TOKENS_PER_CONSULT` | `16000` | Hard token budget per consult |
| `AUTO_TERMINATE_ON_AUDIT_COMPLETE` | `false` | Enable early termination |

---

## 6. API Endpoints

### Existing (unchanged signatures)
| Route | Method | Description |
|-------|--------|-------------|
| `/v1/turn` | POST | Execute a user turn |
| `/v1/agent-call` | POST | Execute an agent call with preflight validation |
| `/v1/branch/fork` | POST | Fork a branch |
| `/v1/session/{id}/replay` | GET | Replay a session |
| `/healthz` | GET | Health check |
| `/metrics` | GET | Metrics snapshot |

### New
| Route | Method | Description |
|-------|--------|-------------|
| `/v1/dream` | POST | Trigger or check dream daemon status |

#### `/v1/dream` Request
```json
{
  "session_id": "demo",
  "trigger": true
}
```

#### `/v1/dream` Response
```json
{
  "ok": true,
  "triggered": true,
  "result": {
    "topics_created": ["task_hello"],
    "topics_updated": [],
    "topics_pruned": [],
    "summary": "Pattern analysis...",
    "duration_ms": 5,
    "session_count": 10,
    "events_reviewed": 42
  },
  "dream_count": 1,
  "dream_enabled": true
}
```

---

## 7. Metrics

Extended `/metrics` endpoint with new counters:

| Metric | Type | Description |
|--------|------|-------------|
| `recovery_rate` | float | Ratio of successful recovery attempts |
| `recovery_attempts` | int | Total recovery attempts |
| `dream_frequency` | float | Dream execution frequency |
| `dream_executions` | int | Total dream passes executed |
| `compaction_ratio` | float | Average compaction ratio |
| `compactions` | int | Total compaction passes |
| `cache_hit_rate` | float | RAM index cache hit rate |

---

## 8. New Event Kinds

All new event kinds are recorded on the consult event spine for full replayability:

| Kind | Description |
|------|-------------|
| `recovery.attempt` | Self-healing loop recovery attempt |
| `recovery.fallback` | Self-healing loop model fallback |
| `context.compact` | Context compaction event |
| `dream.result` | Dream daemon reflective pass result |
| `audit.decision` | Self-audit termination decision |

These kinds are excluded from egress (never sent to the model) per the `isNeverEgressKind` function.

---

## 9. Configuration

All new v0.3.0 options are available as environment variables and serve-mode flags. See `.env.example` for the complete list.

### Serve Mode Flags
```
harness serve [flags]

New v0.3.0 flags:
  -dream-enabled          enable dream daemon (default: false)
  -dream-inactivity-hrs   hours before dreaming (default: 24)
  -dream-min-sessions     min sessions before dreaming (default: 5)
  -dream-topic-dir        directory for topic memory files
  -max-tokens-per-consult max tokens per consult (default: 16000)
  -auto-terminate-audit   auto-terminate on audit complete (default: false)
  -memory-index-size      max entries in RAM index (default: 1024)
  -compaction-turns       trigger compaction every N turns (default: 10)
  -compaction-ratio       trigger at this budget ratio (default: 0.8)
```

---

## 10. Backward Compatibility

- All existing benchmarks, demo-cli replay/fork commands, and tests pass unchanged
- New features are fully covered by the event spine (replays work perfectly)
- No increase in binary size or runtime dependencies beyond what's already in the Nix flake
- Existing HTTP API endpoints maintain identical signatures and behavior
- Existing sessions and replays are 100% backward compatible

---

## 11. Nix Integration

The dream daemon is first-class in `flake.nix`:

```bash
# Build the dream daemon
nix build .#dream

# Run it
nix run .#dream -- -force -session-id demo

# Or use the harness with dream enabled
nix run .#harness -- serve -dream-enabled -dream-topic-dir /tmp/topics
```
