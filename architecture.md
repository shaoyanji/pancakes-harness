# architecture.md

# Contract-Defined Execution for a Local-First Agent Harness

## 1. Thesis

The reliable unit of the system is not the final artifact. It is the **contracted execution step** that produced it.

For this project, that thesis is applied to a local-first chatbot/agent harness with a strict remote transport budget. The local runtime owns memory, lineage, replay, and tool state. The remote model is treated as a **stateless reasoning executor** that receives only a small assembled working set.

The design target is therefore not “keep a chatbot in the provider context window.” The design target is:

- replayability
- failure localization
- bounded re-execution
- process-level idempotency
- strict byte-budget compliance on remote calls

## 2. System split

The system has three planes.

### Memory plane

The local event graph is the source of truth.
It stores turns, branch operations, tool requests and results, summaries, packet decisions, and replay records.

### Scheduling plane

The runtime chooses which local facts matter now.
It ranks frontier items, unresolved deltas, recent turns, and tool outputs to produce a tiny working set.

### Transport plane

Remote calls are small, stateless, and budgeted.
The system ships compact handles, summaries, and selected excerpts rather than raw full history.

## 3. Why this exists

Without a local execution model, mixed LLM/tool systems tend to behave like this:

- a small upstream change forces too much re-execution
- identical outputs obscure meaningfully different process histories
- failures are only visible at the final artifact
- the system cannot explain what must be rerun and what can be reused
- remote context size becomes the effective architecture

This project rejects that model.

The provider context window is not the system memory model.
The local event/branch graph is.

## 4. Core objects

### Blob

Immutable byte payload addressed by hash. Large content, proofs, logs, tool outputs, and serialized summaries reduce to blobs.

### Packet

Instantiated work unit. A packet binds exact target, scope, forbidden scope, constraints, referenced inputs, and the contract under which it is to be executed.

### Contract

Stage-local agreement. It defines admissible input shape, executor class, projection rule, verifier rule, determinism class, and invalidation behavior.

### Record

Append-only fact of one stage execution. A record binds packet, contract, executor identity, exact inputs, exact outputs, projection digest, verdict, stop reason, and cost metadata.

### Trace

Linked structure formed by records. A trace may be linear or DAG-shaped.

### Branch

A branch is a pointer-based conversational/work lineage. It is not a copied transcript. It is a head pointer plus summary basis plus unresolved deltas.

## 5. Contract-defined equivalence

Raw hashes answer whether bytes differ. They do not answer whether a stage-relevant equivalence class changed.

For that, each contract defines a **projection rule**.

A projection rule is a deterministic function from normalized stage inputs to a canonical serialized value whose digest is used for contract-defined equivalence.

This digest is the **projection digest**.

The system invalidates or replays based on:

- projection digest changes
- contract version changes
- executor identity changes when the contract treats them as significant
- missing or invalid verifier requirements

This allows the runtime to avoid coarse rebuilds when irrelevant upstream data changes.

## 6. Record over artifact

Two identical artifacts may be operationally different if they were produced by:

- different packets
- different contracts
- different executors
- different lineage paths

Therefore, the artifact alone is not the trusted unit.
The **recorded execution step** is.

This is what makes the system locally explainable:

- where failure began
- what remained reusable
- what changed materially
- what changed only incidentally

## 7. Applying this to the 14 KB harness

The remote model call is just one contracted execution step.

It must obey a hard envelope budget:

- request line
- headers
- JSON body
- total under 14,336 bytes

That means the system cannot treat remote requests as full transcript replays.
It must instead:

- checkpoint history locally
- encode branch handles compactly
- select a small frontier
- ship summaries and minimal excerpts
- fail closed on oversize packets

The local system is stateful.
The remote step is stateless.

## 8. Branches, checkpoints, and frontier assembly

Conversation context is modeled as:

- branch head
- base summary/checkpoint
- unresolved delta range
- selected frontier items

The transcript is not the context.
The **branch head plus checkpoint plus frontier** is the context.

That is the compression spine.

This enables:

- cheap forks
- alternate tool paths
- replay from trusted boundaries
- bounded re-execution
- tiny outbound packets even for long local sessions

## 9. Tool model

Tools are not part of the core harness language model loop.
They are external executors.

The harness records:

- tool request packet
- tool executor identity
- exact inputs
- exact outputs or failures
- compact summaries for later reuse

Only the minimal relevant slice of tool output should re-enter the next remote packet.
Large outputs remain local.

## 10. Visible surface vs hidden runtime

The visible surface should stay small.

First-class visible objects:

- packets
- contracts
- records
- traces
- branches
- blobs

The visible operator language should also stay small.
Blocking sequence and parallel alternatives are enough for v0.

Deeper runtime policy may later include:

- heap-like reprioritization
- frontier scheduling
- branch speculation
- deferred consolidation

But those are implementation details until the visible invariants are stable.

## 11. Failure model

A good failure is:

- local
- attributable
- replayable

### Local

The system can identify the first bad record, not merely observe a bad final artifact.

### Attributable

The system can name the packet, contract, executor identity, parent records, and exact inputs involved.

### Replayable

The system can resume from the last trusted boundary instead of rebuilding from origin.

Distinct stop reasons matter. The system should not collapse everything into “failed.”
Examples:

- timeout
- budget exhaustion
- verifier missing
- invalid schema
- explicit abort
- projection mismatch

## 12. Local explainability

The goal is not framework maximalism.
The goal is that the system can say something useful when it breaks.

Instead of saying:

> the output changed

it should be able to say:

> this contracted step, under this executor and this packet, produced a projection mismatch; these parent records remain valid; replay can start here.

That is the operational payoff of this architecture.

## 13. v0 boundary

For v0, the system only needs enough machinery to prove the following:

- records can be emitted for local and remote steps
- projection digests can drive invalidation and replay
- branches can fork without transcript copying
- summaries can checkpoint history
- packet assembly can enforce the 14 KB transport contract deterministically
- tool steps can be recorded and replayed through a stable protocol
- backend storage can be swapped behind an interface

Anything beyond that is optional.

## 14. Closing compression

The primary object of trust is the **contracted execution step**, not the artifact.

The local-first agent harness should therefore preserve:

- exact inputs
- exact outputs
- contract identity
- executor identity
- projection digest
- verdict
- stop reason
- lineage

This makes the system cheaper to refine, easier to explain when broken, and more reusable under changing intent than a design centered on rehydrating full context for every pass.

## 15. Local service surface: serve API and agent ingress

The harness may expose a local HTTP service surface for human turns and inter-agent calls. This surface is not the model boundary. It is an ingress boundary into the local runtime.

Two ingress classes are expected:

- human turn ingress (`/v1/turn`)
- agent ingress (`/v1/agent-call`)

Both map into the same runtime/session core. Neither should accept raw transcript blobs or raw model packet bodies as the primary contract.

Ingress may remain richer than egress. The harness should reconstruct local context from persisted state and only enforce the strict 14KB constraint at model egress.

This preserves the design goal:

- local state remains canonical
- cluster callers pass intent + handles
- the harness owns context reconstruction, packet shaping, and model egress policy

### `/v1/agent-call` ingress contract freeze (current)

`/v1/agent-call` is now treated as a narrow contract boundary with deterministic preflight normalization, stabilized fingerprinting, and coalesced completion semantics.

Current invariants:

- malformed boundary input returns structured `400` JSON (`malformed_boundary_input`).
- valid unresolved intent is not executed and returns unresolved metadata (`resolved=false`, `missing` populated) without a fabricated consult artifact.
- resolved intent computes the request fingerprint only after preflight normalization.
- normalized-equivalent inputs must yield identical fingerprints.
- concurrent normalized-equivalent requests share one leader execution and every waiter receives the exact leader-completed payload for that fingerprint.
- resolved responses carry a consult manifest aligned with stabilized identity + normalized intent, with explicit byte accounting.
- this contract does not alter `/v1/turn` behavior.
