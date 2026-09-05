# Opt-in telemetry design

Owner requirement: "make sure the user opts in, not do it without asking them at any cost".

## Consent state machine

Missing, corrupt, unreadable or unknown-schema state is undecided and off. Only an explicit terminal answer transitions to granted. A negative answer or `telemetry disable` transitions to declined, remembered across upgrades. No timer, script, CI marker, environment value or --yes can grant consent. The TUI never prompts over insert mode or another modal, or during a storage reload. A clipped question cannot accept yes. CLI JSON mode verifies its disclosure stream is a TTY.

Consent binds to the exact endpoint and schema shown. A mismatch suspends sending until fresh consent, with a new random ID and empty counters. Config and hard-off environment switches cannot grant consent. The exact shared prompt is in consent.go and reproduced in [TELEMETRY.md](../TELEMETRY.md).

## Data and concurrency

State contains schema, consent, consent endpoint/version/day, revision, random ID, counters, last attempted/acknowledged days and exact acknowledged payload. It is shared across profiles in the effective data directory, mode 0600. Durable atomic replacement prevents partial state. A stable sibling advisory lock covers read/modify/write and the bounded request across processes. Revision comparisons reject stale affirmative saves. Negative choices reload and save under the lock. An already-started request may finish before disable returns, at most five seconds; a completed disable prevents subsequent sends unless consent is explicitly renewed. Counter recording uses a nonblocking lock and drops samples while another operation holds it, so a sender never stalls the UI through a counter hook. Manual edits, rollback and deletion bypass the concurrency/daily-history guarantees; deletion resets off.

## Payload and transport

TELEMETRY.md and CLI --help enumerate the exact fields and counter allowlist. Unknown keys are dropped twice; versions and IDs are validated; counts saturate at 1000; dates are UTC days; the body is bounded at 2 KiB. Only interactive TUI code calls MaybeSend. It rechecks consent, environment and interactivity and durably reserves the day before POST. No retries that day, redirects, cookies, proxy env, response interpretation or CLI sends. Acknowledgement loss is unknown delivery.

## Backend decision and threat model

The .invalid placeholder remains undeployed and triggers no network or DNS. A self-hosted endpoint requires HTTPS, except loopback test HTTP; URL credentials, queries and fragments are refused. Operators must validate the strict schema, cap bodies, suppress IP/header/body logs throughout their proxy/CDN/hosting stack, and publish retention/deletion policies. No running receiver is claimed here. The client cannot enforce server retention. Transport necessarily reveals a source IP, and pseudonymous IDs, rare usage and timing can correlate reports. Rotation removes the explicit ID link, not every possible correlation. A local account attacker who can rewrite consent state can forge it; that is outside this boundary.

## Verification

Tests cover no-consent paths, hard-off switches, automation, endpoint/schema changes, payload values, CLI JSON/EOF, clipped prompts, stale decisions and cross-process count/send/disable schedules. Tests and contributor self-check run only in bounded Docker or CI. A separate agent reviews consent and privacy.
