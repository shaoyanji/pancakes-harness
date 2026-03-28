# CHANGELOG

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
