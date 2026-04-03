# CHANGELOG

## v0.2.3

Release date: 2026-04-03

pancakes-harness v0.2.3 makes consult outcomes durable.

Highlights:

- Added `consult.resolved` and `consult.unresolved` event kinds to the local event spine.
- Resolved `/v1/agent-call` requests now append durable consult events on the existing WAL/replay spine.
- Coalesced agent-calls now persist intelligible leader and follower consult events, with follower events pointing back to the leader consult event id.
- Replay surfaces consult events as first-class history facts alongside session/branch state.
- Consult events are summary-grade: outcome, role, fingerprint, session/branch identity, refs, byte accounting, serializer version. No artifact dump.
- Branchless unresolved scope failures remain response-local in `v0.2.3` so the event spine does not fabricate branch identity.
- Updated architecture docs, README, and PLANS to name consult records as a first-class visible object.

This release completes the first step of the v0.2.x durability arc: typed ingress → deterministic consult identity → replayable consult record.

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
