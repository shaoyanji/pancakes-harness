# pancakes-harness

pancakes-harness is a local-first context and egress kernel. It reconstructs local consult context, shapes a bounded model-facing artifact, preserves replayable branch state, and exposes a thin ingress API. It is intentionally not the full agent execution/control plane.

This repository provides a thin core that:

- persists session/branch state locally
- rebuilds context from local state
- assembles model-bound packets under a strict envelope budget
- exposes a small local HTTP API (`/v1/turn`, `/v1/agent-call`, replay/health/metrics)

Release line: `v0.2.0`

This repository does not provide the full agent policy layer (approvals, sandbox policy, orchestration strategy, cluster scheduler, or UI).

## What This Is

- Context broker for local agents/users.
- Model egress gateway with deterministic packet assembly and budget enforcement.
- Replayable local event graph with branch support.
- Swappable backend/model/tool boundaries.

## What This Is Not

- Full autonomous agent policy/control plane.
- Transcript-forwarding layer that ships full conversation history by default.
- Distributed scheduler or fleet coordinator.
- Heavy observability stack.

## Architectural Invariants

- Locally stateful, remotely stateless.
- Outbound model request budget is hard-capped at 14,336 bytes (request line + headers + body).
- Source of truth is local event/branch graph, not provider context window.
- Branches are pointer-based (not transcript copies).
- Packet assembly/compaction is deterministic.
- No silent truncation.
- Tools are external to core harness.

## Quick Start

### 1) Mock (one-shot)

```bash
CGO_ENABLED=0 go run ./cmd/harness -model-mode mock "hello harness"
```

### 2) Ollama + memory backend (one-shot)

Start Ollama and pull a model:

```bash
ollama serve
ollama pull qwen3:0.6b
```

Run harness:

```bash
CGO_ENABLED=0 go run ./cmd/harness \
  -model-mode ollama \
  -ollama-endpoint http://127.0.0.1:11434 \
  -ollama-model qwen3:0.6b \
  -backend-mode memory \
  -session-id demo \
  -branch-id main \
  "hello harness"
```

### 3) Ollama + xs backend (one-shot)

```bash
CGO_ENABLED=0 go run ./cmd/harness \
  -model-mode ollama \
  -ollama-endpoint http://127.0.0.1:11434 \
  -ollama-model qwen3:0.6b \
  -backend-mode xs \
  -xs-command xs \
  -session-id demo \
  -branch-id main \
  "hello harness"
```

### 4) Serve mode

```bash
CGO_ENABLED=0 go run ./cmd/harness serve \
  -model-mode ollama \
  -ollama-endpoint http://127.0.0.1:11434 \
  -ollama-model qwen3:0.6b \
  -backend-mode memory \
  -bind 127.0.0.1 \
  -port 8080
```

## Demo CLI (`cmd/demo-cli`)

Small line-oriented demo surface over existing HTTP seams. It does not add runtime logic and is intentionally just a thin shell over the local HTTP API.

Help and version:

```bash
go run ./cmd/demo-cli --help
go run ./cmd/demo-cli --version
```

Run:

```bash
go run ./cmd/demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main
```

Commands:

- plain text: sends `/v1/turn` (or `/v1/agent-call` if mode is `agent`)
- `:agent <text>` -> `/v1/agent-call`
- `:fork <name>` -> `/v1/branch/fork`
- `:replay` -> `/v1/session/{id}/replay`
- `:status`
- `:mode <turn|agent>`
- `:quit`

The CLI defaults to `turn` mode. Use `--mode agent` or `:mode agent` to send plain text to `/v1/agent-call` instead.

Raw JSON mode:

```bash
go run ./cmd/demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main --json
```

### Demo flow: normal turn

```bash
go run ./cmd/demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main <<'EOF'
hello harness
:quit
EOF
```

### Demo flow: agent-call resolved (compact kernel summary)

```bash
go run ./cmd/demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id main --mode agent <<'EOF'
summarize latest state in one sentence
:quit
EOF
```

Example compact agent line:

```text
[demo/main] agent resolved fp=4fb0d5a1e2c3 consult=yes bytes=640/14336 answer=...
```

### Demo flow: agent-call unresolved

```bash
go run ./cmd/demo-cli --addr http://127.0.0.1:8080 --session-id demo --branch-id "" --mode agent <<'EOF'
summarize latest state in one sentence
:quit
EOF
```

Example compact unresolved line:

```text
[demo/] agent unresolved fp=- consult=no missing=scope
```

## HTTP API

### `POST /v1/turn`

Request:

```json
{
  "session_id": "demo",
  "branch_id": "main",
  "text": "hello harness"
}
```

Response:

```json
{
  "session_id": "demo",
  "branch_id": "main",
  "answer": "demo response",
  "decision": "answer",
  "envelope_bytes": 225
}
```

Example:

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/turn \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"demo","branch_id":"main","text":"hello harness"}'
```

### `POST /v1/agent-call`

Request:

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

Response:

```json
{
  "session_id": "demo",
  "branch_id": "main",
  "decision": "answer",
  "resolved": true,
  "fingerprint": "stable-normalized-fingerprint",
  "reason": "Reply in one brief sentence.",
  "answer": "...",
  "tool_calls": [],
  "envelope_bytes": 225,
  "trace": {
    "packet_event_id": "...",
    "response_event_id": "...",
    "consult_manifest": {
      "session_id": "demo",
      "branch_id": "main",
      "fingerprint": "stable-normalized-fingerprint",
      "mode": "agent_call",
      "scope": "main",
      "refs": ["branch:head", "tool:last"],
      "constraints": {
        "max_sentences": "1",
        "reply_style": "\"brief\""
      },
      "selected_items": [
        { "id": "branch:head", "kind": "ref", "ref": "branch:head", "bytes": 11 },
        { "id": "tool:last", "kind": "ref", "ref": "tool:last", "bytes": 9 }
      ],
      "byte_budget": 14336,
      "actual_bytes": 640,
      "compacted": false,
      "truncated": false,
      "serializer_version": "consult_manifest.v1",
      "task_summary": "Reply in one brief sentence."
    }
  }
}
```

Example:

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/agent-call \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"demo","branch_id":"main","task":"Reply in one brief sentence.","refs":["branch:head"],"constraints":{"reply_style":"brief","max_sentences":1},"allow_tools":false}'
```

Notes:

- `refs` are optional hints in v1.
- `allow_tools=false` blocks tool execution; if the model requests tools, API returns a clean error.
- `branch_id` is required for a resolved agent-call path; empty scope stays valid but unresolved and does not fabricate a consult manifest.

#### Agent-call contract invariants

- malformed boundary input returns structured `400` JSON (`error.code = malformed_boundary_input`).
- valid unresolved intent returns `200` with `decision = unresolved`, `resolved = false`, populated `missing`, and no fabricated consult artifact.
- resolved intent computes `fingerprint` from normalized post-preflight intent (not raw request ordering/spacing).
- normalized-equivalent requests produce identical fingerprints.
- concurrent normalized-equivalent requests coalesce to one execution; all waiters receive the exact same completed payload bytes.
- resolved responses include a consult manifest aligned to stabilized identity/normalized intent (`session_id`, `branch_id`, `fingerprint`, `mode`, `scope`, `refs`, `constraints`) with explicit byte accounting (`byte_budget`, `actual_bytes`).
- `/v1/turn` contract remains unaffected by `/v1/agent-call` ingress/coalescing behavior.

## Metrics

`GET /metrics` returns local JSON metrics (no Prometheus dependency).

Example:

```bash
curl -sS http://127.0.0.1:8080/metrics
```

Typical fields include:

- `requests_total` by route
- `errors_total` by route
- `packet_budget_rejections_total`
- `compaction_stage_counts`
- latencies (`turn`, `agent_call`, `model_call`, `replay`, `tool_call`, `packet_assembly`)
- `envelope_bytes`, `body_bytes`
- `backend_mode`, `model_mode`

## Benchmark Scripts

### `scripts/benchmark_compare.sh`

Simple latency comparison:

- direct Ollama API call
- harness `/v1/turn`
- harness `/v1/agent-call`

Run:

```bash
N=3 \
OLLAMA_ENDPOINT=http://127.0.0.1:11434 \
OLLAMA_MODEL=qwen3:0.6b \
HARNESS_URL=http://127.0.0.1:8080 \
./scripts/benchmark_compare.sh
```

### `scripts/benchmark_context_growth.sh`

Context-growth benchmark for scenarios (`linear`, `noisy`, `tool_heavy`, optional `branched`):

- direct naive full-context Ollama baseline
- harness `/v1/turn`
- harness `/v1/agent-call`

Outputs CSV with latency, envelope bytes (harness), direct request body bytes, output text, correctness, and compaction-stage hints.

Run:

```bash
HARNESS_URL=http://127.0.0.1:8080 \
OLLAMA_ENDPOINT=http://127.0.0.1:11434 \
OLLAMA_MODEL=qwen3:0.6b \
SCENARIOS="linear noisy tool_heavy branched" \
SIZES="4 8 16" \
RUNS=1 \
OUTPUT_FILE=/tmp/context_growth.csv \
./scripts/benchmark_context_growth.sh
```

### `scripts/benchmark_context_growth_reduced.sh`

Reduced matrix helper for repeatable larger runs:

- scenarios: `branched tool_heavy noisy`
- sizes: `16 32 64 128`

Run:

```bash
HARNESS_URL=http://127.0.0.1:8080 \
OLLAMA_ENDPOINT=http://127.0.0.1:11434 \
OLLAMA_MODEL=qwen3:0.6b \
RUNS=1 \
OUTPUT_FILE=/tmp/context_growth_reduced.csv \
./scripts/benchmark_context_growth_reduced.sh
```

### `scripts/benchmark_report.sh`

Post-processes context-growth CSV output into a markdown report with:

- per scenario/size/path median latency
- timeout counts
- loose and strict correctness pass rates
- average/max envelope bytes
- average/max request body bytes
- dominant compaction stage
- anomaly counts (extra-text, non-ASCII, possible language drift)

Run:

```bash
./scripts/benchmark_report.sh /tmp/context_growth.csv /tmp/context_growth_report.md
```

Or print to stdout only:

```bash
./scripts/benchmark_report.sh /tmp/context_growth.csv
```

## Benchmark Methodology

- Warm model once before timing.
- For each scenario/size, build synthetic history before measured calls.
- Compare three paths:
  - direct naive full-context call to Ollama
  - harness `/v1/turn`
  - harness `/v1/agent-call`
- Evaluate correctness via benchmark token checks:
  - loose correctness: token present / reported pass
  - strict correctness: exact expected token only
- Collect latency, egress envelope bytes (harness), direct request body bytes, and compaction-stage hints.

## Benchmark Caveats And Interpretation

- Direct baseline and harness are intentionally different egress strategies; absolute latency alone is insufficient.
- Strict correctness is the stronger signal for instruction adherence; loose correctness can mask extra-text behavior.
- Timeout spikes can dominate medians at low run counts; use larger `RUNS` for stable comparisons.
- Non-ASCII and language-drift anomaly flags are heuristics for operator review, not definitive model-quality judgments.
- Compaction stage dominance helps identify when context growth starts forcing aggressive egress reduction.

## Troubleshooting

### `CGO_ENABLED=0`

If your environment/toolchain is inconsistent, use:

```bash
CGO_ENABLED=0 go run ./cmd/harness ...
```

This often avoids local linker/cgo friction for operator demos.

### Ollama timeouts

- Increase `HARNESS_MODEL_TIMEOUT` (or `-model-timeout`) for slower local models.
- Keep model warm (`ollama serve` running; benchmark warmup enabled).
- Reduce scenario sizes in benchmark scripts (`SIZES`, `RUNS`) if host is constrained.

### Malformed model response

If adapter output is not valid internal schema JSON, runtime will reject it cleanly (e.g. malformed/invalid-schema paths). Verify:

- adapter mode and endpoint/model are correct
- Ollama model is compatible with strict JSON output requirements
- no proxy/middleware is rewriting response payload

## Known-Good Baseline Config

Minimal reliable baseline for local operators:

- model: Ollama `qwen3:0.6b`
- backend: `memory` for quick tests, `xs` for backend-integration checks
- serve bind: `127.0.0.1`
- timeout: `120s` for local CPU-only setups

Example environment (see `.env.example`):

```bash
HARNESS_MODEL_MODE=ollama
HARNESS_OLLAMA_ENDPOINT=http://127.0.0.1:11434
HARNESS_OLLAMA_MODEL=qwen3:0.6b
HARNESS_BACKEND_MODE=memory
HARNESS_MODEL_TIMEOUT=120s
HARNESS_SERVE_BIND=127.0.0.1
HARNESS_SERVE_PORT=8080
```

## Binary Help

Both shipped binaries expose explicit help/version text for release verification:

```bash
go run ./cmd/harness -h
go run ./cmd/harness -version
go run ./cmd/harness serve -h
go run ./cmd/demo-cli --help
go run ./cmd/demo-cli --version
```

## Operator Guidance

Treat this repo as a thin context/egress harness:

- pass intent + handles/refs into harness APIs
- let runtime reconstruct and compact context locally
- keep high-level agent policy and orchestration outside this repo

This separation is the main maintenance advantage for fleet operations.

### Highlights

- Added a typed preflight boundary for `/v1/agent-call`
- Normalized-equivalent requests now derive the same stabilized fingerprint
- In-flight coalescing now returns the exact leader-completed payload to equivalent waiters
- Added deterministic consult manifest generation with byte accounting
- Resolved `/v1/agent-call` responses now attach a consult manifest in trace output
- Froze and documented the current `/v1/agent-call` contract invariants
- Added `cmd/demo-cli`, a small turn-based demo shell over the HTTP API
- Added explicit `--help` and `--version` behavior for both binaries
- Added a minimal Nix flake packaging surface for cacheable CI builds
