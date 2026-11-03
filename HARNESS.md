# pancakes-harness v0.4.0 — AI Harness Guide

This document describes the v0.4.0 harness with self-healing, context compaction, three-layer memory, dreaming, self-auditing, and two-tier preprocessing.

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

## 9. Two-Tier Preprocessing Pipeline

The harness supports a fast pre-processing sidecar that enriches ingress messages before the main model loop runs. The architecture separates extraction (fast model) from routing (strong model) to minimize latency while preserving decision quality.

### Design

┌──────────┐     ┌─────────────────┐     ┌─────────────────┐
│  Ingress  │────▶│  Fast Model     │────▶│  Event Spine    │
│  (text)   │     │  (extraction)   │     │  (durable)      │
└──────────┘     └────────┬────────┘     └─────────────────┘
                          │
                   ShouldRouteToStrong()?
                          │
                 ┌────────┴────────┐
                 │                 │
            ┌────▼────┐     ┌─────▼──────┐
            │  Skip   │     │ Strong     │
            │ (direct)│     │ Model      │
            └─────────┘     │ (routing)  │
                            └────────────┘

### Golden Schema (`internal/preprocess/types.go`)

The fast model produces an `Extraction` — pure enrichment, no decisions:

| Field              | Type       | Constraint                         |
|--------------------|------------|------------------------------------|
| `intent`           | enum       | question, command, status_update, artifact_share, conversation, correction, unknown |
| `intent_confidence`| float      | 0.0–1.0                           |
| `entities`         | []Entity   | name (non-empty), type enum, confidence, match_type |
| `topics`           | []TopicTag | code, debugging, planning, infra, data, review, meta, general |
| `sentiment`        | enum       | neutral, positive, frustrated, urgent |
| `sentiment_confidence` | float  | 0.0–1.0                           |
| `summary`          | string     | max 200 chars                     |
| `flags`            | []Flag     | uncertain, multi_intent, ambiguous_entity, low_confidence, needs_review |

The strong model produces a `Routing` — decisions only:

| Field              | Type       | Description                       |
|--------------------|------------|------------------------------------|
| `intent`           | IntentClass| confirmed or revised intent        |
| `suggested_tool`   | string     | tool to invoke                     |
| `target_agent`     | string     | agent to route to                  |
| `priority`         | enum       | low, medium, high                  |
| `requires_context` | bool       | needs store/memory lookup          |
| `reasoning`        | string     | why this routing was chosen        |

Both are combined in an `Envelope` with processing metadata (model names, latency, memory query time).

### Background Daemon (`internal/preprocess/daemon.go`)

The fast preprocessor runs as a concurrent sidecar, not a synchronous call:

- **Channel-based event loop** with configurable worker pool
- **Fail-fast**: hard timeout per job (default 2s), errors never block the caller
- **Stackable**: results accumulate for debugging/inspection via `Results()`
- **Non-blocking submit**: returns false if queue is full (caller skips preprocessing)
- **Spine integration**: records `preprocess.extraction` events on success only
- **OnResult callback**: for memory indexing or triggering downstream routing

### Strict vs Best-Effort Mode

The Groq adapter supports two modes via `Strict` config:

| Mode          | Behavior                                              | Use Case                       |
|---------------|-------------------------------------------------------|--------------------------------|
| Strict (default) | Constrained decoding, guaranteed valid JSON, no flags | Production — deterministic     |
| Best-effort   | Model can set flags like `multi_intent`, `uncertain`  | Debugging — escape hatch routing |

`ShouldRouteToStrong()` returns true when:
- Any flag is set (uncertain, multi_intent, needs_review)
- Intent confidence < 0.5

### Groq Adapter (`internal/preprocess/groq.go`)

- Endpoint: `POST https://api.groq.com/openai/v1/chat/completions`
- Model: `openai/gpt-oss-20b` (supports strict mode)
- Structured output via `response_format.json_schema` with `strict: true`
- Auth: `GROQ_API_KEY` env var
- Temperature: 0.1 (extraction consistency)
- Latency: 300–700ms per message

### Integration with Session

The daemon is wired into `runtime/session.go` via `Config.Preprocessor`:

```go
// In handleUserTurn, after recording turn.user event:
if s.preprocessor != nil {
    s.preprocessor.Submit(preprocess.Job{
        ID:        eventID,
        SessionID: s.id,
        BranchID:  branchID,
        Text:      text,
        TS:        now,
    })
}
```

The session continues to packet assembly immediately — preprocessing runs in the background and its results are available for the next turn or for memory indexing.

### Local backend: needle3 + laya (no API key)

The `FastAdapter` seam also has a local implementation. `-preprocess needle`
starts `scripts/needle_bridge.py` as a long-lived child process and speaks JSON
lines with it: one request per ingress turn, one record per line back.

| Stage | Model | Cost (measured, 2 vCPU codespace) |
| :--- | :--- | :--- |
| extraction | Needle 3 (Cactus Compute, 2-bit, 8-29 MB `.cact`) | 0.3–3.0 s, ~130 MB RSS |
| gate | laya (System One decision engine, `scripts/vendor/systemone_engine.py`) | <1 ms, stdlib only |

Both are local: no key, no per-token cost, and the weights are fetched once
(35 MB) and then run with `HF_HUB_OFFLINE=1`.

What needle3 actually does here, stated plainly because it shapes the design:

- **It is a tool caller, not a chat model.** `run()` refuses a message that is
  not a request for an action (0 of 1 command-style samples produced a record),
  so the bridge uses `extract()`, which records the message itself (2 of 3
  samples produced a record; the miss is recorded as `uncertain`).
- **The constrained decoder guarantees shape, not values.** With an enum-typed
  dataclass schema every field parses, but a free numeric field comes back on
  the model's own scale — `95.0` where the schema means `0.95`. The bridge
  normalizes above 1 as a percentage and clamps to 0.0–1.0.
- **`strict=True` rejects extraction outright.** The grounding validator calls
  every derived field fabricated (a confidence is not a substring of the
  message), so the bridge calls `extract(..., strict=False)`.
- **Topics need trimming.** Unconstrained, the model answers with all eight
  topic tags; the bridge keeps the first three.

The laya gate answers one question — *act on this locally, or escalate?* — with
its own calibrated confidence. It only escalates above `0.75` (laya's own
`min_confidence_threshold`), so in practice it lets most turns through and
flags the rest with `needs_review`; a measured gate on ordinary messages reads
`act` at 0.50–0.70. It is a second opinion, not a second model.

The whole loop is one command:

```bash
NEEDLE_PYTHON=~/needle-venv/bin/python scripts/ritual_demo.sh
```

It serves the harness with `-preprocess needle`, posts one turn, and prints the
sidecar record — extraction latency, the gate decision, and the flags the record
carries. The sidecar is async by design, so its line arrives after the turn
response, on the bridge's stderr.

### Fine-tuning it on the harness's own tools

`scripts/needle_finetune.sh` turns the four typed tools in `internal/tooling`
(`glob`, `grep`, `read`, `write`) into a training set, trains a LoRA adapter on a
12-layer rung of needle3, and scores the base rung and each adapter through the
`.cact` engine on a held-out split. Measured on a 2-core / 8 GB codespace, CPU
only, 160 held-out rows of which 152 are callable and 11 need two calls:

| | base rung | 1 epoch | 2 epochs | 3 epochs | 4 epochs |
| :--- | ---: | ---: | ---: | ---: | ---: |
| correct tool set | 17.1% | 27.0% | 42.1% | 59.2% | **59.9%** |
| exact arguments | 7.9% | 11.2% | 21.7% | **39.5%** | 38.2% |
| exact call | 7.9% | 5.3% | 21.1% | **39.5%** | 38.2% |
| └ single-call rows (n=141) | 8.5% | 5.7% | 22.7% | **42.6%** | 41.1% |
| └ multi-call: right call count | 2/11 | 7/11 | 11/11 | 9/11 | 8/11 |
| └ multi-call: right tools | 0/11 | 0/11 | 0/11 | 0/11 | 0/11 |
| refusals correct (n=8) | 100% | 100% | 100% | **0%** | 0% |
| parse failures (of 160) | 0 | 0 | 0 | 2 | 0 |
| latency p50 | 334 ms | 216 ms | 210 ms | 196 ms | 228 ms |
| trainer's own held-out | — | 5/30 | 17/30 | — | — |

Every column is the same pinned corpus, the same 12-layer rung and the same 160
rows; only the epoch count (and the cosine schedule that follows it) differs. Val
losses are **not** comparable across columns (a 4-epoch run decays over 572 steps
where a 2-epoch run decays over 286), so only the bench rates are read across rows.

Six things the numbers mean, in the order they matter for using this:

- **One epoch is a trap, and two is under-trained.** At one epoch the tool-choice
  rate nearly doubles (17.1% → 27.0%) while *exact* calls fall below the base rung
  (7.9% → 5.3%). The adapter has learned to answer but not yet to copy, and it now
  attempts rows the base rung declined, so its precision drops on a denominator
  that grew. Judged on exact calls only, the 1-epoch adapter is worse than no
  adapter at all — which is why an accuracy number read at one epoch is not a
  result. Two epochs looked like the answer here until three were run: 21.1% →
  39.5% exact calls, nearly doubling again on the same data.
- **Argument binding peaks at three epochs and then stops.** Epoch 4 buys 0.7 pp
  of tool choice (59.2% → 59.9%) and loses 1.3 pp of exact calls (39.5% → 38.2%,
  two rows — inside noise), while the trainer's val loss keeps falling (0.0741 →
  0.0704) and its training loss reaches 0.0196. The loss curve has stopped
  tracking the task: it is memorising the training set, not binding better. On
  this corpus and phrase set the ceiling is ≈40% exact calls, reached at epoch 3.
  Three epochs is also the point where the corpus stops teaching extraction and
  starts teaching "always emit a call" (see the refusal row).
- **The third epoch costs every refusal.** Refusals go 100% → 0% between two and
  three epochs: all eight out-of-scope rows ("what should I have for dinner",
  "summarise the history of the Byzantine empire") come back as a confident
  `grep` or `read`. Those rows are 8 of 600 training examples, and at three passes
  the adapter has fitted the far more common "answer with a call" pattern
  instead. So the 39.5% is bought with the calibration that made §9's `uncertain`
  flag meaningful — a caller checking `abstain_rate` must not use this adapter
  alone as its gate.
- **The stock model does not answer a command-style request at all.** Asked to
  "cat the contents of internal/preprocess/needle.go", the untuned archive
  returns `function_calls: []` and reasons *"no document retrieval tool
  available"* — the tool list is right there in its prompt. Emitting a call for
  an imperative is the behaviour the adapter adds.
- **Schema-shaped data fixed argument keys; values are the remaining axis.** The
  earlier flat `{"name": "read", "args": {"ref": "string"}}` tool block produced
  **0.0%** exact arguments because nothing tied a value to a key, and the model
  invented `pattern` / `file_name` / `file` in its place. With
  `parameters.properties` kept, the 2-epoch adapter invents **zero** unknown keys
  across all 152 callable rows: it now always speaks the schema's vocabulary.
  What it still gets wrong is the value. At three epochs 92 of the 152 callable
  rows are wrong, and the five largest classes say the residue is lexical, not
  semantic:

  | failure at 3 epochs | n | example |
  | :--- | ---: | :--- |
  | right tool, no arguments | 9 | `fetch the file internal/server/server.go` → `read` with `{}` |
  | `grep` → `glob` | 19 | `locate vendorHash inside "…"` → `glob` |
  | `glob`, `/*.go` dropped | 10 | `what .go files live in internal/memory` → `pattern: internal/memory` |
  | `write` → `grep` / `read` | 16 | `create X containing Y` → `grep` with the content as pattern |
  | `grep`, `pattern`/`text` swapped | 3 | `search "A" for B` → `pattern: A, text: B` |

  The `grep` → `glob` class alone is 19 of those 92 failures: "locate X inside Y"
  is a text-scoped search, and the adapter reads "locate X" as a filename guess.
  That and the `*.go` collapse look mechanical enough that a post-processor
  (expand a bare `internal/X` to `internal/X/*.go`, un-swap `pattern`/`text`)
  appears to move the same adapter from 39.5% to 46.7% without retraining. **It
  does not, and that number must not be quoted.** `scripts/needle_rituals.py`
  gives every verb family a disjoint `train` and `eval` rendering list, so *all
  160 eval rows are phrasings the corpus never shows*, and those three rules are
  keyed to the eval renderings themselves (`locate … inside`, `what .go files
  live in …`). A resolver written from these failures is a hand-labeller for the
  test set. The honest reading is therefore the opposite of a win: 39.5% exact on
  fully held-out paraphrases is a *paraphrase-generalization* result, and the
  large failure classes are exactly the families whose eval rendering shares the
  least vocabulary with its train rendering (`grep`: "find X in the text" /
  "where does X appear in" → "locate X inside"; `glob`: "list {pattern}" → "what
  .go files live in {dir}"). The lever is rendering diversity in the corpus,
  not post-processing in front of the model. The same holdout explains the
  refusal collapse: train's three out-of-scope strings and eval's three are
  disjoint, so 30 of 600 rows could not survive three passes.
- **`write` is not reachable from the model.** 16 `write` rows, **0 exact at every
  epoch**. It comes back as `grep` with the content string as the pattern, or as
  `read`. Four epochs do start naming the tool (6/16 rows mention `write`, up from
  1) but only as a second call after a `read` of the same file, and never with the
  `ref` + `content` pair the schema wants — the phrasings ("create X containing
  Y", "put Y into X") read like a search with a quoted needle. Two causes stack
  here: those are held-out renderings too (train gets "write \"…\" to {path}" /
  "save \"…\" at {path}"), and the row asks the model to copy a content string
  into an argument, which is the one thing 60 of 600 rows cannot teach. Route
  writes deterministically; do not try to tune this away with the current
  corpus.
- **Multi-call structure is learned, pairing is not — and it degrades past two
  epochs.** Every gold multi-call row is `read` + `glob`, and the 2-epoch adapter
  emits exactly two calls 11/11 times — but never that pair (it proposes
  `read`+`write`, `grep`+`write`, …). At three and four epochs the count slips
  (9/11, 8/11) and two rows are truncated mid-call at the token budget, while the
  pairing is still 0/11. The count is a shape it can infer; which tools go
  together is a relation, and 42 multi-call training rows do not carry it.
- **Depth is a memory wall, not a quality choice.** A 20-layer training compile
  needs about 4.5 GB and is killed on this runner (SIGTERM, ~4.5 GB RSS); 12
  layers peaks at 3.9 GB. Every column is the same rung, so the delta is only the
  adapter. 143 steps per epoch cost ≈20 min on two cores: 286 steps (2 epochs)
  run in ≈41 min, 572 steps (4 epochs) in ≈79 min, and the build plus a bench
  adds ≈6 min.

The tool schemas are dumped from the Go registry rather than hand-written, so the
dataset cannot drift from the code it describes:

```bash
go run ./scripts/dumptools > scripts/needle_tools.json
PY=.venv-train/bin/python NEEDLE=.venv-train/bin/needle EPOCHS=3 \
  bash scripts/needle_finetune.sh
```

`scripts/needle_rituals.py` pins the file pool to `scripts/needle-data/corpus.json`
on first use (`--refresh-corpus` re-pins deliberately): splitting by a sha1 of each
value keeps the held-out set disjoint as the repo grows, but a pool that grew with
the tree moved three eval rows between runs, and two runs a day apart were then
not scored on the same set. The pipeline prints the sha1 of `train.jsonl`,
`eval.jsonl` and `corpus.json` so a reported number can be traced to the data that
produced it. Everything it writes — `checkpoints/` (150 MB rung) and
`scripts/needle-data/` (35 MB base `.cact`, 39 MB tuned, the adapters, the
predictions) — is gitignored and rebuilt by that command.

The training venv is not committed (`cactus-needle[train]` wants python 3.12 and
pulls jax, flax and optax):

```bash
virtualenv -p python3.12 .venv-train
.venv-train/bin/pip install 'cactus-needle[train]==3.2.9'
```

The tuned archive is a tool caller, not an extraction model: point
`NEEDLE_WEIGHTS` at it for a tool-calling workload, and leave §9's sidecar on the
published weights, which are what the `Extraction` schema was trained against.
39.5% exact calls at 196 ms is a router that still needs a table around it:
validate every call against the tool's JSON schema before running it, keep
`write` and any two-call ritual behind a deterministic resolver, and do not read
its abstention as a gate (the 3-epoch adapter never declines).

Both adapters are archived outside the repo, because rebuilding them is ~40 min
of CPU and the codespace they were trained on is disposable: `lora-ep3`
(sha256 `50ea53492af12032519828ac4cbbcb8f23ee565295139035b74e09815f193b5e`) and
`lora-ep4` (sha256 `4292b988f2ae560fae27d9d94fe43aeccc2fe8ceff57008360bb88641d882696`),
4.5 MB each, alongside their merged `.cact` exports and per-run predictions.
Drive is the store of record; the archive is a convenience, not a dependency.

---

## 10. Configuration

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

## 11. Backward Compatibility

- All existing benchmarks, demo-cli replay/fork commands, and tests pass unchanged
- New features are fully covered by the event spine (replays work perfectly)
- No increase in binary size or runtime dependencies beyond what's already in the Nix flake
- Existing HTTP API endpoints maintain identical signatures and behavior
- Existing sessions and replays are 100% backward compatible

---

## 12. Nix Integration

The dream daemon is first-class in `flake.nix`:

```bash
# Build the dream daemon
nix build .#dream

# Run it
nix run .#dream -- -force -session-id demo

# Or use the harness with dream enabled
nix run .#harness -- serve -dream-enabled -dream-topic-dir /tmp/topics
```

---

## 13. Future Considerations

### Local Model Fallback

The Groq adapter is the primary fast model backend, but the `FastAdapter` interface is backend-agnostic. A local model adapter (llama.cpp, Ollama) would provide:
- Offline preprocessing when Groq is unavailable or rate-limited
- Schema adherence without API latency for development/testing
- Same golden schema contract regardless of backend

The daemon's fail-fast design means a local fallback adapter can be swapped in without changing the event loop or spine integration. `internal/preprocess/needle.go`
(see §9) is that adapter for Needle 3, and it needs no API key at all.

### Embedding Model Integration

Groq inference at 1000 tok/s makes extraction fast, but embedding models offer a complementary capability:
- **Semantic indexing**: embed entity names, topics, and summaries for vector similarity search in the memory layer
- **Decoherence shaping**: raw embedding vectors need projection into the structured schema — the extraction schema could serve as the target shape for embedding-to-structure pipelines
- **Hybrid scoring**: combine recency (current `ScoreEvents` heuristic) with semantic relevance from embeddings for better compaction and working set selection

The memory layer's `EmbedFunc` interface already exists for pluggable relevance scoring. Embedding models would plug in there, with the extraction schema providing the structured metadata that embeddings operate on.

### Schema Form Adherence

The strict/best-effort split in the Groq adapter points toward a general pattern:
- Strict mode for deterministic extraction (production)
- Best-effort for uncertainty signaling (debugging, exploration)
- Local model fallback for both modes when API latency or cost is a concern

A `SchemaValidator` interface could enforce golden schema conformance regardless of backend — validating that any model output (Groq, local, future providers) conforms to the same contract before it reaches the event spine.
