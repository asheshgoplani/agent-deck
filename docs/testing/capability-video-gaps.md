# Capability videos: honest gaps

This file records which capability flows the VHS video gallery
(`docs/status/capability-videos.html`, rendered by
`scripts/capability-videos.sh`) can and cannot show authentically. It follows
the same principle as `docs/testing/capability-gaps.md`: a flow we cannot record
running the real binary is documented as a gap, never re-enacted with hardcoded
output.

The videos record the real `agent-deck` binary against the deterministic
echobot agent (the Wave 1 stand-in) in an isolated sandbox. They are paced for
watchability but the on-screen output is genuine: the echo reply is read back
live with `session output`, not typed into the tape.

## What the clips show authentically

| Clip | Flow | Genuine on-screen proof |
|------|------|-------------------------|
| `echo-roundtrip` | launch, send token, read reply, teardown | the real `ECHO:PING-DEMO-7f3a` reply from the echobot |
| `lifecycle-launch` | atomic add + start + send in one command | the registry row plus the echoed launch token |
| `lifecycle-stop` | start, stop, state flips to stopped | `list` before and after the real `session stop` |
| `lifecycle-fork` | fork precondition guard | the real refusal error for an unsupported session |

## Gaps (not recorded, by design)

### Fork that inherits tool context
`lifecycle-fork.tape` shows the precondition guard refusing to fork an unsupported
session, which is the deterministic half of the capability. The
context-inheriting happy paths need real tool session data — a live
`ClaudeSessionID` for Claude or a Pi JSONL for `pi --fork` — so they cannot be
recorded offline. This is the same Tier N gap documented for the asserting test
in `docs/testing/capability-gaps.md` (Fork that inherits tool context).

### Real agent round trips (claude, codex, gemini, opencode)
The `echo-roundtrip` clip uses the deterministic echobot so the reply is stable
and the recording reproducible. Recording a real agent replying would need that
CLI installed plus its auth, and the reply is non-deterministic (bad for a
committed artifact). The echobot proxy is what keeps the clip honest about the
wiring; the nightly real-agent tests guard drift between the proxy and reality.

### Why not record the Go capability test directly
The Go suite (`tests/capability/...`) drives tmux headless, at machine speed,
with no client rendering frames. Capturing that is unwatchable. The videos
instead drive the same real binary and the same echobot, paced by VHS, so a
human can actually watch the token go out and the reply come back. The Go tests
remain the source of truth for pass/fail; the videos are the watchable companion.

## Regenerating

```bash
# Outside tmux (the script refuses inside a live tmux unless isolated):
scripts/capability-videos.sh

# On a truly headless host (no X display):
xvfb-run -a scripts/capability-videos.sh

# A single clip:
scripts/capability-videos.sh echo-roundtrip
```
