# CHANGELOG

## v0.2.4

Release date: 2026-04-03

pancakes-harness v0.2.4 is explainable egress selection.

This release deepens the consult kernel without widening it into retrieval machinery. Resolved consults now carry a compact, machine-readable selector explanation that makes pre-compaction selection auditable before packet assembly disappears behind a provider call.

Resolved consult manifests and replayable consult events now expose:

- per-selected-item inclusion reasons
- a bounded excluded-item sample with exclusion reasons
- dominant inclusion and exclusion signals for the consult overall
- a narrow budget-pressure flag when selection had to survive compaction pressure

The reason taxonomy stays stable and narrow. The shipped codes reflect only signals the current selector actually computes, including:

- `branch_locality`
- `recent_turn`
- `tool_result`
- `summary_checkpoint`
- `checkpoint_ref`
- `global_relevant`
- `budget_fit`
- `debug_never`
- `non_local`
- `sensitive_local`
- `ref_unavailable`

Replay, demo, and benchmark-reporting surfaces now summarize selector behavior without dumping giant candidate lists or contaminating outbound packets with explainability payloads. `/v1/turn` remains unchanged.

What this release does not do:

- no smart-retrieval framework
- no LLM-ranked selection layer
- no plugin or policy system
- no broad public API redesign
- no giant selector dumps

Intentional deferral:

full serializer unification across consult manifest and consult event models remains a `v0.2.5` task, and local export/review remains a later thin-surface pass.

## v0.2.3

Release date: 2026-04-03

pancakes-harness v0.2.3 makes consult activity durable on the existing local event spine.

This release adds narrow, replayable consult records for `/v1/agent-call` without widening the kernel into a framework or introducing a new storage subsystem. Resolved consults now persist first-class consult events with stable summary-grade metadata such as fingerprint, contract version, outcome, role, byte accounting, and task summary. Coalesced followers append their own linked follower consult events so replay can distinguish leader/follower behavior cleanly.

Replay and demo surfaces now expose consult activity as first-class history rather than leaving it response-local. At the same time, consult durability is explicitly excluded from future egress selection so the event spine does not contaminate outbound packet assembly.

What this release does not do:

- no plugin contract
- no new backend/store abstraction
- no scheduler/workflow layer
- no broad serializer unification
- no explainable selection payloads yet

Intentional deferral:

branchless unresolved consult failures remain response-local because the current event spine requires a non-empty branch identity, and this release does not relax that invariant or fabricate synthetic scope.

This release completes the durability step of the consult-kernel arc and sets up the next pass: explainable egress selection.

## v0.2.2

Release date: 2026-04-01

pancakes-harness v0.2.2 is a toolchain coherence pass.

Highlights:

- Made `nix develop` the canonical Go development environment.
- Defined authoritative Go 1.23 toolchain in `flake.nix` (matching `go.mod`).
- All package builds (`.#harness`, `.#demo-cli`, `.#tests`) use the same Go toolchain.
- Updated README to document `nix develop` as the authoritative Go environment.

Release verification checklist:

- `nix develop -c go version` reports Go 1.23
- `nix build .#harness` passes
- `nix build .#demo-cli` passes
- `nix flake check` passes
- `go test ./...` passes
- `go build ./...` passes

## v0.2.1

Release date: 2026-04-01

pancakes-harness v0.2.1 is a coherence pass focused on inspectability and demo ergonomics.

Highlights:

- Added `:help`, `:json on|off`, `:manifest`, and `:trace`/`:last` commands to `cmd/demo-cli`.
- Added explicit `contract` version field to `/v1/agent-call` responses (`agent_call.v1`).
- Consult manifest serializer version stabilized at `consult_manifest.v1`.
- Improved compact resolved/unresolved output formatting in demo CLI.
- Documented Nix flake usage (`nix run .#harness`, `nix run .#demo-cli`, `nix flake check`).
- Added narrow tests to freeze contract field behavior.

Release verification checklist:

- `go test ./...`
- `go build ./...`
- `cmd/harness` and `cmd/demo-cli` help/version output verified
- README aligned to current shipped behavior

## v0.2.0

Release date: 2026-03-28

pancakes-harness is a local-first context and egress kernel. It reconstructs local consult context, shapes a bounded model-facing artifact, preserves replayable branch state, and exposes a thin ingress API. It is intentionally not the full agent execution/control plane.

Highlights:

- Added a typed ingress boundary for `POST /v1/agent-call`.
- Added preflight validation and normalization for the resolved agent-call seam.
- Stabilized normalized fingerprinting so equivalent resolved requests hash to the same identity.
- Hardened coalescing so identical in-flight requests execute once and followers receive the exact leader-completed payload.
- Added deterministic consult manifest generation and attached the manifest on resolved `/v1/agent-call` responses.
- Froze the contract docs for the current seam and documented the thin `cmd/demo-cli` HTTP shell.

Release verification checklist:

- `go test ./...`
- `go build ./...`
- `cmd/harness` and `cmd/demo-cli` help/version output verified
- README aligned to current shipped behavior

Out of scope for this release:

- outbox or fanout execution
- TUI or streaming UI
- broad framework abstractions
- orchestration/control-plane expansion
