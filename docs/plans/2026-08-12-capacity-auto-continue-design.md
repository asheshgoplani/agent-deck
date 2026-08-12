# Capacity auto-continue

Status: approved 2026-08-12

## Motivation

A session can stop at the Claude banner:

```
⚠ Selected model is at capacity. Please try a different model.
```

The operator wants Agent Deck to continue the session once capacity clears,
while retaining the selected model. Today this is classified as
`model-unavailable`, but only observed by self-heal.

## Decision

Extend the existing opt-in self-heal `resume` mode so a confirmed
`model-unavailable` session receives one continuation prompt through the same
verified send path used for transport errors and usage-limit resumes.

- Do not change the session model.
- Do not restart the session.
- Retain the existing 90-second dwell, two-read confirmation, per-session and
  fleet caps, circuit breaker, composer-draft protection, audit trail, and
  opt-out controls.
- A failed continuation is bounded by those existing controls; retries are not
  an unbounded loop.

Configuration remains the existing explicit opt-in:

```toml
[selfheal]
enabled = true
mode = "resume"
```

## Architecture

`internal/tmux` continues to classify the capacity banner as
`model-unavailable`. `internal/selfheal` maps that substate to `ActionResume`
alongside transport and usage-limit cases. The existing resume executor sends a
model-capacity-specific continuation message through the registered,
delivery-verified sender. The transition daemon remains the sole scheduler.

## Verification

Unit tests will prove that:

1. the exact capacity banner is recognized as `model-unavailable`;
2. resume mode authorizes a resume action for that substate without a model
   switch or restart; and
3. the executor sends the capacity continuation prompt and preserves the real
   delivery verdict.

## Out of scope

- Selecting or switching to fallback models.
- New watchdogs, timers, services, or configuration options.
- Changing behavior while self-heal is disabled or in observe mode.
