# Fixture provenance

Claude and Codex replay the existing versioned fixtures under
`cmd/agent-deck/testdata/comms/`. The runner loads them and substitutes disposable session IDs, paths, prompts and text. The source files are not rewritten by the fake.

The other JSON files here are **synthetic protocol fixtures**, derived from the
2026-10-03 harness survey and the production adapters. They are not recordings
of a live model run. Each carries a distinct text marker when its upstream
protocol has text; no text field is invented on Cursor stop or OpenCode idle.

Pi's `turn_end` input is passed to the installed production TypeScript extension
by `pi_driver.mjs`. Version 2 discards the input and emits its own status-only
payload. The fake does not bypass this by calling hook-handler with invented
text. Pi status must be checked after replay because the production extension
intentionally swallows hook-handler process failures.

OpenCode's server exposes `/event`, `/session/status`, `/session/:id`, and
`/session/:id/message`. Its POST `/emit` control endpoint changes the fixture
stream; it is not an agent-deck endpoint. Optional HTTP JSONL receipts prove
which endpoints the production consumer actually contacted. A running fake
server alone does not prove agent-deck consumed its SSE events.

Cursor `afterAgentResponse` is a separate upstream response event, currently
not in the installed hook subscription. Replay it only when explicitly testing
that boundary; do not treat it as evidence of installation coverage.
