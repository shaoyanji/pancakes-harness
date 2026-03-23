# AGENTS.md

This repository builds a **Go-first local agent harness**.

Before making code changes:

- Read `PLANS.md`.
- Use `architecture.md` for conceptual background and invariants.
- Treat `PLANS.md` as the implementation source of truth.
- Keep diffs scoped to the active milestone.
- Prefer small, replayable, test-backed increments.

Project rules:

- The chatbot is **locally stateful** and **remotely stateless**.
- Never solve continuity by shipping full transcripts by default.
- Preserve a strict outbound request budget of **14,336 bytes total** for request line, headers, and JSON body.
- The source of truth is the **local event/branch graph**, not the provider context window.
- Branches must be pointer-based, not copied transcripts.
- Packet assembly must be deterministic.
- No silent truncation.
- Tools are external to the core harness.
- The Go core must remain distributable as the primary compiled binary.

Execution guidance:

- Start with the smallest milestone that produces a runnable, testable slice.
- Prefer interface boundaries before optimizations.
- Do not introduce advanced scheduling, UI work, or distributed execution unless `PLANS.md` explicitly calls for it.
- When unsure, preserve replayability, diagnosability, and bounded re-execution over convenience.

Validation guidance:

- Add or update tests with every milestone.
- Favor golden tests and deterministic fixtures for packet assembly, replay, and invalidation.
- Record schema changes explicitly.

Out of scope unless the plan explicitly expands:

- Full production UI
- Distributed worker orchestration
- Provider-specific caching tricks as the primary memory model
- Using provider token IDs as the core internal context representation
