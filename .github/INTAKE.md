# INTAKE.md — machine-readable intake spec for agent-deck

> Audience: AI agents opening issues or PRs on a human's behalf, and the humans directing them.
> If your agent reads this one file and follows it, your PR or issue passes intake on the first try.
> Human-readable policy lives in [CONTRIBUTING.md](../CONTRIBUTING.md); this file is the field-level contract.

## Principles

1. AI-authored contributions are welcome with equal standing. Disclosure is required; it is a quality signal, never a filter.
2. The one thing intake cannot accept as empty is genuine human intent: what did your human actually ask for? An agent acting for a real person always has this. Quote them.
3. Every required field has an honest escape answer. "unsure" (model), "can't reproduce reliably, happens when ..." (repro), one real sentence (intent). Empty or gutted fields fail intake; honest-but-imperfect answers pass.

## Pull request body contract

Use `.github/PULL_REQUEST_TEMPLATE.md` verbatim. Required sections, by exact heading:

| Heading | Required | Valid content |
|---------|----------|---------------|
| `## What problem does this solve?` | yes | 1+ sentence from the user's point of view; link issue/Discussion if any |
| `## Why this change` | yes | why this approach |
| `## User impact` | yes | "None, internal only" is valid |
| `## Evidence` | yes for behavior changes | real output, terminal capture, or before/after logs; mock-only proof is insufficient for user-visible changes |
| `## AI disclosure` | yes | exactly one checked box of the three; model name(s) if AI helped |
| `## What actually bothered you` | yes | 1+ real sentence of human intent; for agent-opened PRs, quote the human's ask |
| `## Checklist` | yes | check what is true; do not check what is not |

### Machine-readable marker

The last line of the PR body is an HTML comment the intake tooling parses:

```
<!-- gate:ai=<human|assisted|authored> model=<name-or-unsure> intent=<yes|no> -->
```

- `ai`: `human`, `assisted`, or `authored`, matching the checked box above.
- `model`: the primary model id (e.g. `claude-opus-4-x`, `gpt-5-codex`) or `unsure`. Use `none` when `ai=human`.
- `intent`: `yes` if the "What actually bothered you" section carries a real human ask, else `no`.

The visible sections are authoritative; the marker is a parse-stable confirmation. A blank marker never blocks, but a filled one speeds routing.

### Example of a passing PR body (abbreviated)

```markdown
## What problem does this solve?
`agent-deck list` shows stale status for OpenCode sessions after detach. Fixes #1614.

## Why this change
Status came from tmux content sniffing; this switches OpenCode to its SSE endpoint, which is authoritative.

## User impact
OpenCode sessions show correct status within 1s of a state change.

## Evidence
Before/after capture:
    $ agent-deck list   # before: "running" 40s after exit
    $ agent-deck list   # after: "idle" within 1s
Sandboxed suite: ok (all packages).

## AI disclosure
- [x] AI-authored (a model wrote most of it)

**Model(s), if AI helped:** claude-opus-4-x

## What actually bothered you
My human asked: "opencode sessions always show running in the deck even after they finish; can you fix the status?"

## Checklist
- [x] Targeted diff: one problem, no unrelated changes
- [x] Tests added or updated for new behavior
- [x] Test suite passes sandboxed: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...`
- [x] CHANGELOG.md untouched (entries are added at landing)
- [x] AI-assisted? Disclosed above, with validation evidence, and I can answer questions about the code
- [x] "Allow edits from maintainers" is enabled

<!-- gate:ai=authored model=claude-opus-4-x intent=yes -->
```

## Issue contract

Issues use GitHub forms (`.github/ISSUE_TEMPLATE/bug.yml`, `feature.yml`); the form enforces required fields. For agents pre-composing bodies:

- **Bug**: version (`agent-deck version` output), OS/terminal, exact repro steps, expected vs actual, redacted logs (strip tokens, emails, private paths). AI-disclosure dropdown + model.
- **Feature**: the problem situation (not just the feature), proposed behavior, alternatives, willingness to implement. AI-disclosure dropdown + model.
- Questions and setup help go to [Discussions](https://github.com/asheshgoplani/agent-deck/discussions), not issues.

## Self-check before opening

Run your draft PR body through this checklist locally so the review loop is never needed:

```bash
# All required headings present?
for h in "What problem does this solve?" "Why this change" "User impact" "AI disclosure" "What actually bothered you" "Checklist"; do
  grep -q "^## $h" pr-body.md || echo "MISSING: $h"
done
# Exactly one AI-disclosure box checked?
[ "$(grep -c '^- \[x\]' <(sed -n '/^## AI disclosure/,/^## /p' pr-body.md))" = "1" ] || echo "CHECK: AI disclosure"
# Intent section non-empty?
sed -n '/^## What actually bothered you/,/^## /p' pr-body.md | grep -vq '^\(##\|<!--\|$\)' || echo "MISSING: human intent"
```

And before pushing code:

```bash
gofmt -l ./cmd ./internal            # must print nothing
go vet ./...
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

## House rules an agent must respect

- Maximum 5 open PRs per author at a time.
- Diffs over ~3000 added lines need a linked issue or Discussion first.
- One problem per PR. Never edit CHANGELOG.md.
- Never mark `CRITICAL`/`SECURITY` without a concrete repro; real security issues go through [SECURITY.md](../SECURITY.md), not a public PR.
- Merges are always human. Validation results arrive as a structured comment within about a day.
