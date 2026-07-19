# pr-body-template.md — one complete passing PR body

> Copy the structure exactly: the intake gate parses `## ` headings
> character-for-character, and the last line must be the gate marker. This
> example is a realistic bug-fix PR; replace the content, keep every heading.

```markdown
## What problem does this solve?

`agent-deck list` keeps showing `running` for OpenCode sessions up to 40s after
the session actually finished, so users attach to dead sessions. Fixes #1614.

## Why this change

Status for OpenCode came from tmux pane-content sniffing, which only updates on
the next poll tick. OpenCode exposes an authoritative SSE status endpoint; this
switches the OpenCode preset to consume it and falls back to sniffing only when
the endpoint is unreachable. I tried shortening the poll interval first, but
that raised idle CPU for every session type to fix one tool — scoped this to the
OpenCode preset instead.

## User impact

OpenCode sessions show the correct status within ~1s of a state change. No
change for any other tool preset.

## Evidence

Reproduction on main (v1.9.71), then with this branch:

    # before: session exited at 14:02:11, list still shows running
    $ agent-deck list
    opencode-fix   running   ~/src/app      # 14:02:49

    # after: same scenario
    $ agent-deck list
    opencode-fix   idle      ~/src/app      # 14:02:12

Sandboxed suite: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...` → ok, all packages.

Revert-check: with the non-test hunks reverted, `TestOpenCodeStatusFromSSE`
fails as expected (`status_test.go:88: got "running", want "idle"`), so the
test proves the fix.

## AI disclosure

- [x] AI-authored (a model wrote most of it)

**Model(s), if AI helped:** claude-opus-4-x

**Prompt / session log (optional):** —

## What actually bothered you

My human asked: "opencode sessions always show running in the deck even after
they finish — can you fix the status so I stop attaching to dead sessions?"

## Checklist

- [x] Targeted diff: one problem, no unrelated changes
- [x] Tests added or updated for new behavior
- [x] Test suite passes sandboxed: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...`
- [x] If this touches a hot path (list, status, session output, startup, tmux layer): before/after timing evidence included
- [x] CHANGELOG.md untouched (entries are added at landing)
- [x] AI-assisted? Disclosed above, with validation evidence, and I can answer questions about the code
- [x] "Allow edits from maintainers" is enabled

<!-- gate:ai=authored model=claude-opus-4-x intent=yes -->
```

## Rules the example demonstrates

1. **Headings verbatim** — all six required sections present, exact `## ` text.
2. **Exactly one AI-disclosure box checked**, model named (`unsure` is also
   valid; blank is not; `model=none` only when `ai=human`).
3. **Intent is a real quoted human ask** — the one field intake cannot accept
   blank. An agent with a real user always has this.
4. **Evidence is observed output**, including the revert-check result — not
   claims, not mock-only proof.
5. **Checklist boxes only where true.** An unchecked box with an honest note
   beats a false check every time.
6. **Marker is the last line**, real values, agreeing with the checkboxes.
7. **Timing box checked because `status` is a hot path** — the before/after
   capture doubles as the timing evidence; for perf-sensitive diffs add
   explicit `time agent-deck list` runs.
