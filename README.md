Yes — here’s a README draft aimed at **operators and agents in your fleet**, not marketing.

You can paste this as `README.md` and trim names/paths as needed.

````md
# pancakes-harness

Local-first context harness for LLM and agent workflows.

This repo is **not** a full agent shell. It is the **context plane** and **model egress gate** that other agents can rely on.

Its job is to:

- persist local session and branch state
- reconstruct context from local state
- assemble compact model-bound packets
- enforce a strict egress envelope limit
- expose a thin local API for turns and agent calls
- keep backend/model/tool boundaries swappable

Its job is **not** to own high-level tool policy, approvals, sandboxing, bwrap rules, or agent-specific execution controls. Those belong at the **agent layer above the harness**.

---

## Why this exists

Large agent systems often fail at the boundary between:

- rich local context
- constrained model calls
- reusable replayable state
- cluster-local agent coordination

This harness exists so agents in the fleet do **not** have to improvise their own egress shaping, prompt packing, replay logic, or transcript shipping.

The design goal is:

> local ingress may be rich; model egress must stay constrained.

The harness should be the one place that:

- reconstructs local context
- applies packet compaction
- enforces the model egress budget
- persists results back to local state

That keeps policy tinkering and prompt bloat out of individual agents.

---

## Core mental model

Treat the system as two layers:

### Harness layer = context plane

Responsible for:

- session state
- branch state
- replay
- summaries/checkpoints
- packet assembly
- strict model egress budget enforcement
- local service surface

### Agent layer = execution / control plane

Responsible for:

- tool policy
- sandboxing / bwrap
- approval rules
- retry/escalation
- capability gating
- orchestration strategy
- safety controls

Do **not** collapse these layers without a very good reason.

---

## Current status

This repo currently supports:

- local session runtime
- branch fork + replay
- deterministic packet assembly with hard envelope cap
- tool subprocess protocol
- swappable backend
- swappable model adapters
- local `serve` API
- local Ollama integration
- xs-backed persistence
- cluster-facing `/v1/agent-call`

Known-good local demo path:

- Ollama
- `qwen3:0.6b`
- memory backend or xs backend

---

## Non-goals

This repo is intentionally **not**:

- a policy-heavy agent shell
- a general workflow engine
- a scheduler/orchestrator for the whole fleet
- the place to embed bwrap rules and per-agent safety logic
- the place to ship full transcript blobs between agents

If you are about to add:

- approval systems
- sandbox policy
- browser automation policy
- capability escalation logic
- cluster-wide scheduling

that probably belongs **outside** this repo.

---

## Architectural invariants

These are the rules to preserve.

### 1. Local state is canonical

Conversation/tool/branch state lives locally, not in remote model context.

### 2. Model calls are stateless

The model sees only the compact packet built for the current turn.

### 3. Egress is constrained

Model-bound packets are measured and compacted before send. The strict cap is enforced at model egress.

### 4. Ingress is not egress

Human turns and agent ingress requests are **not** raw model packets.

### 5. The harness owns context reconstruction

Agents should pass intent + handles + refs, not full transcript history.

### 6. The harness is thin

Do not move high-level agent execution policy into this layer.

### 7. Boundaries matter

Keep backend, model, tool, and runtime concerns separated.

---

## Repo layout

The exact file tree may evolve, but the important packages are:

- `internal/runtime` — session orchestration and turn loop
- `internal/replay` — rebuild/replay from persisted events
- `internal/branchdag` — branch state and fork behavior
- `internal/summaries` — checkpoint summaries
- `internal/assembler` — packet assembly, measurement, compaction
- `internal/tools` — subprocess tool protocol
- `internal/backend` — backend interface + memory backend
- `internal/backend/xs` — xs adapter
- `internal/model` — model interface + adapters
- `internal/server` — local HTTP API
- `cmd/harness` — CLI entrypoint and `serve` mode

Treat these as boundaries, not suggestions.

---

## How to think about packet flow

There are three distinct packet types.

### 1. Ingress request

What a human or another agent sends to the harness.

Examples:

- `/v1/turn`
- `/v1/agent-call`

These are requests **to the harness**, not model packets.

### 2. Internal context packet

Built by the runtime from session state, branch state, replay state, summaries, and refs.

This is the harness’ working representation.

### 3. Egress packet

The actual model-bound packet.

This is the one that must be:

- compact
- measured
- deterministic
- budget-enforced

Keep these three distinct.

---

## Quick start

### 1. Mock mode

```bash
CGO_ENABLED=0 go run ./cmd/harness "hello harness"
```
````

### 2. Ollama + memory backend

Start Ollama:

```bash
ollama serve
ollama pull qwen3:0.6b
```

Set `.env.local`:

```bash
HARNESS_MODEL_MODE=ollama
HARNESS_BACKEND_MODE=memory
HARNESS_SESSION_ID=demo
HARNESS_BRANCH_ID=main
HARNESS_OLLAMA_ENDPOINT=http://127.0.0.1:11434
HARNESS_OLLAMA_MODEL=qwen3:0.6b
HARNESS_MODEL_TIMEOUT=120s
HARNESS_XS_COMMAND=xs
```

Run:

```bash
CGO_ENABLED=0 ./run-demo.sh "hello harness"
```

### 3. Ollama + xs backend

Make sure `xs` is available and use the same `.env.local`, but with:

```bash
HARNESS_BACKEND_MODE=xs
```

Then:

```bash
CGO_ENABLED=0 ./run-xs-demo.sh "hello persistent harness"
```

---

## Serve mode

Run the local HTTP service:

```bash
CGO_ENABLED=0 go run ./cmd/harness serve \
  -model-mode ollama \
  -ollama-endpoint http://127.0.0.1:11434 \
  -ollama-model qwen3:0.6b \
  -bind 127.0.0.1 \
  -port 18081
```

### Health

```bash
curl -sS http://127.0.0.1:18081/healthz
```

### Turn API

```bash
curl -sS -X POST http://127.0.0.1:18081/v1/turn \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"demo","branch_id":"main","text":"hello harness"}'
```

### Branch fork

```bash
curl -sS -X POST http://127.0.0.1:18081/v1/branch/fork \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"demo","parent_branch_id":"main","child_branch_id":"alt-1"}'
```

### Replay

```bash
curl -sS http://127.0.0.1:18081/v1/session/demo/replay
```

---

## Cluster-facing agent ingress

The harness exposes:

```http
POST /v1/agent-call
```

This is for other local agents in the fleet.

### Request shape

```json
{
  "session_id": "demo",
  "branch_id": "main",
  "task": "Summarize the latest tool result for the user in one sentence.",
  "refs": ["branch:head", "tool:last"],
  "constraints": {
    "reply_style": "brief",
    "max_sentences": 1
  },
  "allow_tools": false
}
```

### Response shape

```json
{
  "session_id": "demo",
  "branch_id": "main",
  "decision": "answer",
  "answer": "...",
  "tool_calls": [],
  "envelope_bytes": 225,
  "trace": {
    "packet_event_id": "...",
    "response_event_id": "..."
  }
}
```

### Important rules for fleet agents

Other agents should **not** send:

- full transcript history
- giant raw JSON context blobs
- raw model packet bodies

Other agents **should** send:

- session handle
- branch handle
- current task
- optional refs
- optional constraints

The harness will reconstruct context locally and enforce model egress constraints.

---

## Backend modes

### `memory`

Good for:

- tests
- smoke runs
- non-persistent demos

### `xs`

Good for:

- persistent local state
- branch replay across runs
- service mode
- cluster-local harness behavior

When using xs, the harness still owns packet shaping and egress policy. xs is backing state, not a substitute for the runtime.

---

## Model modes

### `mock`

Use for:

- deterministic tests
- no-network runs
- behavior debugging

### `http`

Generic thin HTTP slot. Use only if you know the adapter/request shape is compatible with your provider.

### `ollama`

Known-good local path.

Recommended default for local runs:

- `qwen3:0.6b`

Treat other models as opt-in until validated against the harness’ structured response expectations.

---

## Tool model

The harness supports a thin subprocess protocol for tools.

That does **not** mean the harness should become the final tool policy authority.

Keep these separate:

### Harness tool support

- subprocess request/response schema
- timeout handling
- normalized tool result/failure events
- persistence-friendly storage

### Agent-layer tool control

- whether a tool may run
- sandboxing
- bwrap / jails
- approvals
- network / file access policy
- escalation

If you need tool safety policy, put it above the harness.

---

## For maintainers and agents in the fleet

Before changing anything, ask:

### Am I changing the context plane or the execution plane?

If it is:

- replay
- branch state
- packet shaping
- egress budget
- local ingress API
- backend/model/tool boundaries

then it may belong here.

If it is:

- approval policy
- sandboxing
- capability gating
- safety/policy logic
- orchestration heuristics
- cluster scheduling

then it probably belongs in an agent layer above this repo.

### Am I widening ingress or polluting egress?

Ingress may be rich.

Egress must stay:

- compact
- measured
- deterministic
- model-oriented

### Am I preserving the boundaries?

Do not:

- move provider-specific logic into runtime
- move backend-specific logic into runtime
- move orchestration policy into server handlers
- let agents bypass the harness by shipping raw transcript blobs

---

## Testing

Run the full suite:

```bash
go test ./...
```

Useful focused runs:

```bash
go test ./internal/runtime
go test ./internal/server
go test ./internal/model
go test ./internal/assembler
```

On systems without a C toolchain:

```bash
CGO_ENABLED=0 go test ./...
```

---

## Troubleshooting

### `cgo: C compiler "gcc" not found`

Use:

```bash
CGO_ENABLED=0 go run ./cmd/harness "hello harness"
```

### Ollama timeout

Check:

- `ollama serve` is running
- the model is pulled
- timeout is long enough
- the configured model is the same one you tested manually

Known-good warmup:

```bash
ollama run qwen3:0.6b "hello"
```

### `malformed model response`

Usually means the adapter got bytes back that did not match the required structured response shape.

Check:

- model choice
- adapter prompt/schema behavior
- whether the model is adding prose around JSON

### HTTP 404 / connection refused

Usually means:

- wrong endpoint path
- server not running
- wrong mode or wrong env var

---

## Known good local baseline

Use this when debugging before trying anything fancier:

- backend: `memory`
- model mode: `ollama`
- model: `qwen3:0.6b`
- local bind: `127.0.0.1`
- `CGO_ENABLED=0`

If that path breaks, fix it before debugging cluster-specific behavior.

---

## Extension points

Safe places to extend next:

- interactive `chat` / REPL mode
- richer `/v1/agent-call` constraints
- optional egress serializers (`json | toon | auto`)
- better trace semantics
- improved replay inspection
- more model adapters

Dangerous places to extend casually:

- runtime/session god-object growth
- policy logic in handlers
- provider-specific hacks in core runtime
- backend-specific assumptions outside adapters
- letting ingress become “ship me your whole transcript”

---

## Suggested workflow for fleet agents

When building on this harness:

1. keep the harness thin
2. use `/v1/agent-call` for context-broker behavior
3. let the harness reconstruct context locally
4. let the harness enforce model egress size
5. keep tool/sandbox/approval policy in your own agent layer
6. do not patch around harness egress failures by inflating payloads upstream

If your agent needs more context, improve:

- refs
- summaries
- branch usage
- egress serialization

Do **not** bypass the harness by sending larger raw context blobs.

---

## Final note

This repo is meant to stay boring in the right places.

If you are adding cleverness, make sure it is in service of:

- replayability
- local explainability
- bounded model egress
- reusable context handling

and not just another layer of policy tinkering at the model boundary.
