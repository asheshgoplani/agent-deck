## What problem does this solve?

<!-- One or two sentences, framed from the user's point of view. Link the issue or Discussion if one exists. -->

## Why this change

<!-- Why this approach? Anything you tried that didn't work? -->

## User impact

<!-- What changes for someone running agent-deck? "None, internal only" is a valid answer. -->

## Evidence

<!-- For behavior changes: real output, a terminal capture, or before/after logs. Mock-only proof is not enough for changes users will feel. -->

## AI disclosure

<!-- Pick one. AI PRs are welcome here with equal standing; this is a quality signal, not a gate. -->

- [ ] Human-written
- [ ] AI-assisted (I directed and reviewed it)
- [ ] AI-authored (a model wrote most of it)

**Model(s), if AI helped:** <!-- e.g. claude-opus-4-x, gpt-5-codex, gemini-3-pro. "unsure" is fine. -->

**Prompt / session log (optional):** <!-- a gist or link helps us weight it; not required -->

## What actually bothered you

<!-- In your own words: what were you doing when you hit this? A sentence, a real scenario, a
transcript snippet, a log line. If an agent opened this PR: what did your human actually ask for?
Quote them. This is the first thing a reviewer reads. A one-liner is fine; "" is not. -->

## Checklist

- [ ] Targeted diff: one problem, no unrelated changes
- [ ] Tests added or updated for new behavior
- [ ] Test suite passes sandboxed: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...`
- [ ] If this touches a hot path (list, status, session output, startup, tmux layer): before/after timing evidence included
- [ ] CHANGELOG.md untouched (entries are added at landing)
- [ ] AI-assisted? Disclosed above, with validation evidence, and I can answer questions about the code
- [ ] "Allow edits from maintainers" is enabled

<!-- Your PR will be validated (applied, built, tested) by the maintainer's AI pipeline within about a day. You'll get a structured comment with the result. Merges are always human. -->

<!-- gate:ai=<human|assisted|authored> model=<name-or-unsure> intent=<yes|no> -->
