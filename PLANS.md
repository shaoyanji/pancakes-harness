# PLANS.md

## Project

Build a **Go-based local-first chatbot/agent harness** where conversation, tool results, summaries, and branches are stored locally as an event-sourced graph, and each remote model call is assembled as a **tiny stateless JSON request** under a strict **14,336-byte HTTP envelope**.

The system must support:

- long-running local conversation state
- tool calls
- branch forks
- checkpoint summaries
- deterministic packet compaction
- replay/rebuild from local state
- a swappable `xs`-style backend adapter

## Non-negotiable invariants

- The chatbot is **locally stateful, remotely stateless**.
- Full transcript shipping is forbidden by default.
- Every outbound request must stay below **14,336 bytes total**, including request line, headers, and JSON body.
- The branch/context graph is the source of truth.
- Branch forks are pointer-based, not transcript copies.
- Large content is stored locally and referenced compactly.
- Packet assembly is deterministic.
- No silent truncation.
- Tools are external to the core harness.

## Scope for v0

Build the smallest correct runtime that proves the model:

- local event log
- branch DAG
- checkpoint summaries
- packet assembler with hard byte measurement
- compact branch/context handles
- tool subprocess protocol
- replay/rebuild logic
- swappable event backend interface
- one `xs` adapter implementation
- one model adapter interface with a mock implementation and one real adapter slot

Do **not** build yet:

- advanced scheduling UI
- distributed execution
- speculative branch merging
- complex observability dashboards
- multi-tenant infra
- provider-specific token-tree memory as the primary representation

## Implementation language and boundaries

### Core language

The reference implementation SHALL use **Go** for the core harness.

### Go core includes

- config loading
- event runtime
- branch DAG
- summary checkpoints
- packet budgeting and compaction
- tool orchestration
- model adapter interface
- xs adapter interface and initial implementation
- replay/rebuild
- structured logging
- schema validation

### Tool runtime model

Tools SHALL be language-agnostic subprocesses or sidecars.
The harness MUST NOT require tools to be written in Go.
Tool protocol SHALL be JSON-in / JSON-out.
All tool failures SHALL be normalized into structured events.

## Suggested package layout

```text
cmd/harness/
internal/config/
internal/eventlog/
internal/branchdag/
internal/summaries/
internal/scheduler/
internal/assembler/
internal/model/
internal/tools/
internal/runtime/
internal/backend/
internal/backend/xs/
internal/schema/
internal/replay/
internal/testutil/
```

## Core data model

### Event

Required fields:

- `id`
- `session_id`
- `ts`
- `kind`
- `branch_id`
- `parent_event_id` optional
- `refs` list
- `meta` object
- `blob_ref` optional

Event kinds:

- `turn.user`
- `turn.agent`
- `tool.request`
- `tool.result`
- `tool.failure`
- `summary.checkpoint`
- `summary.rebuild`
- `branch.fork`
- `packet.candidate`
- `packet.sent`
- `packet.rejected_budget`
- `response.received`
- `response.invalid_schema`
- `system.warning`

### Branch

- `branch_id`
- `parent_branch_id` optional
- `fork_event_id` optional
- `head_event_id`
- `base_summary_id`
- `dirty_ranges` list
- `score`
- `materialized_hint` optional

### SummaryCheckpoint

- `summary_id`
- `branch_id`
- `basis_event_id`
- `covered_range`
- `text_ref` or `blob_ref`
- `byte_estimate`
- `token_estimate`
- `freshness_version`

### QueueItem

- `queue_id`
- `branch_id`
- `kind`
- `target_ref`
- `priority`
- `byte_cost_estimate`
- `why`
- `retry_count`

### PacketCandidate

- `session_id`
- `branch_handle`
- `working_set`
- `frontier`
- `constraints`
- `byte_size`
- `compact_stage`

### ModelResponse

- `decision`
- `answer` optional
- `tool_calls` list
- `summary_delta` optional
- `branch_ops` list
- `unresolved_refs` list
- `raw_provider_payload` optional

## Branch/context model

A branch is not a copied transcript.

A branch consists of:

- a `head_event_id`
- a `base_summary_id`
- inherited ancestry via refs
- zero or more dirty ranges
- optional compact dictionary handles

The system must support:

- cheap fork
- replay from summary + delta
- branch-local working set selection
- branch scoring for scheduling

## Encoded branch handle system

Implement a compact local encoding layer for packet assembly.

Purpose:

- reduce branch/context serialization overhead
- provide stable internal handles
- avoid shipping full ancestry
- support caching and deduplication

Requirements:

- maintain a per-session dictionary
- map long ids to short stable symbols
- allow reversible local expansion
- do not require the model to understand the full branch tree
- use handles for transport efficiency, not as the sole source of truth

## Packet budget enforcement

Constants:

- `MAX_ENVELOPE_BYTES = 14336`
- `SAFETY_MARGIN_BYTES = 768`

Algorithm:

1. Assemble headers.
2. Measure actual serialized header size.
3. Compute available body budget.
4. Assemble candidate JSON body.
5. Measure actual serialized body size.
6. If oversized, apply deterministic compaction.
7. Retry until fit or hard failure.
8. On failure, emit `packet.rejected_budget`.

Compaction stages, in order:

1. remove debug fields
2. drop non-essential provenance
3. replace raw excerpts with summary refs
4. collapse multiple deltas into a checkpoint summary
5. shrink working set to newest unresolved frontier
6. replace large text with blob refs only
7. final hard failure

## Tool protocol

### Request

```json
{
  "tool": "tool_name",
  "call_id": "unique-id",
  "args": {},
  "timeout_ms": 15000
}
```

### Success

```json
{
  "ok": true,
  "call_id": "unique-id",
  "result": {},
  "summary": "short summary for model reuse",
  "artifacts": []
}
```

### Failure

```json
{
  "ok": false,
  "call_id": "unique-id",
  "error": {
    "type": "timeout|exec|schema|tool",
    "message": "..."
  }
}
```

Requirements:

- normalize failures into structured events
- store tool outputs locally
- only surface minimal relevant excerpts into the next packet

## xs adapter requirements

The core harness must depend on a backend interface, not directly on `xs` internals.

Configuration precedence:

1. explicit config
2. environment variable
3. default backend config

Required adapter capabilities:

- append event
- append blob/content ref
- read event by id
- read branch/event range
- subscribe/tail session stream
- fetch referenced blob/content
- health check
- clear connectivity diagnostics

The first implementation may use CLI invocation or direct local socket/API integration, but the adapter boundary must stay strict.

## Turn loop

1. receive user turn
2. persist turn locally
3. update branch head
4. enqueue candidate context items
5. choose highest-priority working set
6. assemble packet under budget
7. send stateless model request
8. validate response schema
9. persist response locally
10. either answer, invoke tool(s), or request another compact reasoning turn
11. update summaries/checkpoints as needed

## Milestones

### Milestone 1 — core replay spine

Build:

- event store interface
- file-backed or memory-backed local implementation
- recordable events
- replay from event log
- tests for event append/read/replay

Acceptance:

- can append a session of events
- can rebuild branch head from persisted events
- deterministic replay test passes

### Milestone 2 — branch DAG + summaries

Build:

- branch creation/fork
- base summary pointers
- dirty ranges
- summary checkpoint objects

Acceptance:

- branch fork does not copy transcript
- rebuild from summary + delta passes

### Milestone 3 — packet assembler + budget enforcement

Build:

- serialized header measurement
- body measurement
- deterministic compaction pipeline
- hard reject path

Acceptance:

- packet assembler enforces 14,336-byte cap
- large blobs never ship by default
- compaction is deterministic

### Milestone 4 — tool loop

Build:

- subprocess tool protocol
- timeout handling
- failure normalization
- storage of tool outputs as local refs

Acceptance:

- tool success path works
- tool failure becomes structured event
- follow-up packet only includes minimal tool excerpt

### Milestone 5 — backend adapter

Build:

- backend interface
- xs adapter implementation
- health check and diagnostics

Acceptance:

- runtime logic does not depend on xs specifics
- adapter can be swapped in tests

### Milestone 6 — model integration

Build:

- model adapter interface
- mock adapter
- one real adapter slot
- structured response validation

Acceptance:

- malformed model response is rejected cleanly
- long conversation still results in tiny outbound packets

## Required tests

1. branch fork does not copy transcript
2. replay reconstructs state from events
3. packet assembler enforces 14,336-byte hard cap
4. large blobs never ship by default
5. compaction is deterministic
6. encoded branch handles are reversible locally
7. tool failures become structured events
8. model response schema validation rejects malformed output
9. summary checkpoint rebuild works
10. backend adapter can be swapped without changing runtime logic
11. long conversation still results in tiny outbound packets

## Definition of done for v0

v0 is done when:

- a user can run a local session
- the harness persists turns and tool results locally
- the harness can fork branches cheaply
- the harness can replay and rebuild from local state
- every outbound model request is budget-checked and compacted deterministically
- tool calls work through the external protocol
- backend integration is behind a clean adapter boundary
- the test suite covers the core invariants above

## Post-v0 capability layer — serve API and agent ingress

### Milestone 7 — serve API

Build:

- local HTTP server mode
- loopback-only bind by default
- `POST /v1/turn`
- `POST /v1/branch/fork`
- `GET /v1/session/{id}/replay`
- `GET /healthz`

Acceptance:

- local HTTP requests can drive the existing runtime/session core
- ingress validation is explicit and returns clean JSON errors
- egress budget enforcement remains in runtime/assembler, not handlers

### Milestone 8 — agent ingress

Build:

- `POST /v1/agent-call`
- request shape for:
  - `session_id`
  - `branch_id`
  - `task`
  - optional `refs`
  - optional `constraints`
  - optional `allow_tools`
- response shape for:
  - `decision`
  - `answer`
  - `tool_calls`
  - `envelope_bytes`
  - optional trace refs

Acceptance:

- other local agents can call the harness using intent + handles instead of raw transcript blobs
- unknown refs do not crash the request
- `allow_tools=false` prevents tool execution
- ingress can be richer than egress
- model egress remains budget-checked and compacted deterministically

## Definition of done for serve layer

Serve layer is done when:

- the harness can run as a local HTTP service
- `/v1/turn` works against the existing runtime
- `/v1/agent-call` works as a cluster-facing ingress
- replay and branch operations remain available through the service surface
- ingress stays decoupled from egress packet format
- the 14KB limit is enforced only at model egress, not at local ingress
