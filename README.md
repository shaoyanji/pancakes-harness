# pancakes-harness

pancakes-harness is a local-first context and egress kernel. It reconstructs local consult context, shapes a bounded model-facing artifact, persists a replayable consult record for every resolved consult, and exposes a thin ingress API. It is intentionally not the full agent execution/control plane.

The public promise is:

**typed ingress → deterministic consult identity → replayable consult record → stable serializers**

This repository provides a thin core that:

- persists session/branch/consult state locally
- rebuilds context from local state
- assembles model-bound packets under a strict envelope budget
- exposes a small local HTTP API (`/v1/turn`, `/v1/agent-call`, replay/health/metrics)

Release line: `v0.2.3`

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

## What a Consult Leaves Behind

Every resolved `/v1/agent-call` leaves a **durable consult event** on the local event spine. A consult event is summary-grade, not an artifact dump. It captures:

- session/branch identity and normalized fingerprint
- consult event schema version and agent-call contract version
- resolved vs unresolved outcome
- leader vs follower coalescing role
- selected refs and byte accounting
- consult manifest serializer version when a manifest exists

The response artifact (the immediate answer) answers the caller. The consult event is the receipt — the first-class replayable object. This is what makes the kernel reviewable: any past consult can be replayed, exported, or compared without re-executing the full step.

Consult manifest generation remains deterministic and stable, but it is a renderer-grade artifact derived from the same normalized boundary that produces the durable consult event.

In `v0.2.3`, unresolved consults are durably recorded when the request has a stable branch identity. A branchless unresolved request such as missing `scope` remains response-local for now so the event spine does not fabricate branch identity or relax core event invariants.

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

### 5) Nix

The flake defines the canonical Go toolchain for this project.
Use `nix develop` for a consistent development environment.

```bash
# Enter the canonical development environment
nix develop

# Run harness binary
nix run .#harness -- -model-mode mock "hello harness"

# Run demo-cli
nix run .#demo-cli -- --addr http://127.0.0.1:8080 --session-id demo --branch-id main

# Run tests
nix flake check

# Build binaries
nix build .#harness
nix build .#demo-cli
```

The flake packages:

- `.#harness` - main harness binary
- `.#demo-cli` - demo CLI shell
- `.#tests` - test suite (via `nix flake check`)

Note: `nix develop` sets up the authoritative Go 1.23 toolchain matching `go.mod`.

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
- `:help` -> show command help
- `:json on|off` -> toggle raw JSON output
- `:manifest` -> show last agent-call consult manifest
- `:trace`, `:last` -> show last agent-call raw JSON result
- `:agent <text>` -> `/v1/agent-call`
- `:fork <name>` -> `/v1/branch/fork`
- `:replay` -> `/v1/session/{id}/replay` including durable consult events
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

The `scripts/` directory contains helpers for comparing harness egress against direct naive model calls:

| Script | Purpose |
|--------|---------|
| `benchmark_compare.sh` | Simple latency comparison (direct Ollama vs `/v1/turn` vs `/v1/agent-call`) |
| `benchmark_context_growth.sh` | Context-growth benchmark across scenarios (`linear`, `noisy`, `tool_heavy`, `branched`) |
| `benchmark_context_growth_reduced.sh` | Reduced matrix for repeatable larger runs |
| `benchmark_report.sh` | Post-processes CSV output into a markdown report |

All scripts compare three paths: direct naive full-context call, harness `/v1/turn`, and harness `/v1/agent-call`. They collect latency, egress envelope bytes, direct request body bytes, correctness (loose/strict), and compaction-stage hints.

See the [Benchmark Methodology](#benchmark-methodology) and [Benchmark Caveats](#benchmark-caveats-and-interpretation) sections below for interpretation guidance.

### Quick start

```bash
# Simple comparison (N=3)
N=3 OLLAMA_ENDPOINT=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:0.6b HARNESS_URL=http://127.0.0.1:8080 ./scripts/benchmark_compare.sh

# Context growth report
HARNESS_URL=http://127.0.0.1:8080 OLLAMA_ENDPOINT=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:0.6b SCENARIOS="linear noisy tool_heavy" SIZES="4 8 16" RUNS=1 OUTPUT_FILE=/tmp/context_growth.csv ./scripts/benchmark_context_growth.sh
./scripts/benchmark_report.sh /tmp/context_growth.csv /tmp/context_growth_report.md
```

## Benchmark Methodology

- Warm model once before timing.
- Build synthetic history before measured calls for each scenario/size.
- Compare three paths: direct naive full-context Ollama, harness `/v1/turn`, harness `/v1/agent-call`.
- Evaluate correctness via loose (token present) and strict (exact expected token only) checks.
- Collect latency, egress envelope bytes, direct request body bytes, and compaction-stage hints.

## Benchmark Caveats And Interpretation

- Absolute latency alone is insufficient; direct and harness are intentionally different egress strategies.
- Strict correctness is the stronger signal; loose correctness can mask extra-text behavior.
- Timeout spikes dominate medians at low run counts; use larger `RUNS` for stable comparisons.
- Non-ASCII and language-drift anomaly flags are heuristics, not definitive quality judgments.
- Compaction stage dominance identifies when context growth forces aggressive egress reduction.

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
