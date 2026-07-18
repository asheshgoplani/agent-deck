# gate-spec.md — every check your PR will face, and the exact fix

> Machine-readable companion to [`.github/INTAKE.md`](../../../INTAKE.md). Field
> names and heading strings are exact; the intake Action parses them literally.
> Layers run in order: **intake gate** (instant, on open/edit) → **review
> machine** (four lenses, within ~a day) → **security gates** (merge time) →
> **human merge** (always). A green earlier layer is necessary, never sufficient.

## Layer 1 — intake gate (`.github/workflows/pr-intake.yml`, instant)

Observe-only today: labels + one kind comment, never closes on content. Editing
the PR description re-runs it automatically.

| Check | Condition that fails it | Label | Exact fix |
|---|---|---|---|
| Required headings | any of `## What problem does this solve?`, `## Why this change`, `## User impact`, `## AI disclosure`, `## What actually bothered you`, `## Checklist` absent | `needs-info` | Use `.github/PULL_REQUEST_TEMPLATE.md` verbatim; headings must match character-for-character at `## ` level |
| AI-disclosure box | zero or 2+ checked `- [x]` boxes inside the `## AI disclosure` section | `needs-info` | Check exactly one of Human-written / AI-assisted / AI-authored |
| Human intent | `## What actually bothered you` empty after stripping HTML comments and checkbox lines | `needs-info` | One real sentence. Agent-opened PR: quote the human's ask verbatim. This is the one field intake cannot accept blank |
| Gate marker (informational) | trailing `<!-- gate:ai=... model=... intent=... -->` missing or placeholder | none (slows routing) | Last line of the body, real values, agreeing with the visible checkboxes |
| Clean | none of the above | `intake:clean` | — fast-lane into validation |

Marker grammar: `<!-- gate:ai=<human|assisted|authored> model=<name-or-unsure> intent=<yes|no> -->`
(`model=none` when `ai=human`; visible sections are authoritative on any disagreement).

## Layer 2 — review machine (four lenses, ~a day)

Each lens scores 0–10 with confidence-tiered flags; the verdict is `good`
(validate-and-merge lane), `needs-work` (arrives with 1–3 rank-up moves — do
exactly those, re-review triggers on your push), or `slop` (kind decline; only
genuine no-human-behind-it submissions).

### Cheap mechanical pre-gate (before any lens spends compute)

| Check | Condition | Fix |
|---|---|---|
| Template gutted | no problem statement, no intent, no evidence content at all | fill the template; see Layer 1 |
| Giant undiscussed diff | additions > 3000 AND no linked issue/Discussion | open a Discussion, agree the shape, link it — the check clears |
| Urgency-scanner slop | `CRITICAL`/`URGENT`/`SECURITY` language + scanner-style output + zero repro | add a concrete reproduction, drop the urgency language; real vulns → `SECURITY.md` privately |

### Correctness lens

| Criterion | What it does | How you pre-pass it |
|---|---|---|
| Build + test | applies your diff, builds, runs touched packages sandboxed | `self-check.sh` runs the identical invocation: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...` |
| **Revert-check (centerpiece)** | reverts your non-test hunks and re-runs your tests; a test that still passes proves nothing | write the test first, watch it fail, then fix; `self-check.sh` automates this and put the result in `## Evidence` |
| Diff-coverage spot-check | which changed hunks are exercised by ANY test; untested hunks become named flags | every changed hunk behind at least one test, or say in the body why a hunk is untestable |

### Security lens (diff-scoped security gates)

| Criterion | Trips on | Fix |
|---|---|---|
| Dependency vet (Gate 1) | go.mod/go.sum touched by a community PR; any `replace` directive or GOPROXY override (automatic stop) | avoid new deps when stdlib suffices; if truly needed: canonical upstream, pinned version, justify in body, expect a human gate |
| `.github/` human gate (Gate 2) | any change under `.github/` | never auto-merges — keep out of unrelated PRs and expect the maintainer personally |
| Hidden-logic sweep (Gate 3) | network calls, constructed exec, non-ASCII/homoglyph/invisible chars, env exfil, broad file perms, time-bombs, weak crypto in added lines | run `references/security-self-scan.md` on your own diff before opening |

### Fit lens

| Criterion | Trips on | Fix |
|---|---|---|
| Lean-scope line | net-new tool adapters / bolt-on UIs without demonstrated demand | Discussion first; get a yes before building |
| Overlap | duplicates existing code or a recent merge | check `git log --oneline -30` on main and existing helpers before writing |
| Oversized PR | over ~400 changed lines | split what can split; otherwise name in the body what could split out and why it didn't |
| Style match | code that ignores the surrounding package's conventions | read neighbors first; table-driven tests, existing helpers |
| CHANGELOG edit | any change to CHANGELOG.md | drop it; entries are added at landing |

### Intent lens (slop-vs-genuine)

| Criterion | Question asked | How you pre-pass it |
|---|---|---|
| Disclosure honesty | AI help declared? model named? | one checked box + model name (`unsure` is valid); undisclosed bulk-generation gets closed, disclosed AI gets equal standing |
| Author understanding | does the body show you understand your own change and could defend it? | write `## Why this change` yourself; answer review questions substantively |
| Behavior evidence | real observed behavior (repro, logs, before/after), not just claims? | reproduction capture + before/after output in `## Evidence`; mock-only proof is insufficient for user-visible changes |
| Human need | is there a real human ask behind this? | the quoted ask in `## What actually bothered you` |

## Layer 3 — house rules (enforced across layers)

| Rule | Number | Source |
|---|---|---|
| Open-PR cap | max 5 per author | CONTRIBUTING / INTAKE house rules |
| Big-diff discussion | > 3000 added lines needs linked issue/Discussion | intake pre-gate |
| One problem per PR | 1 | fit lens |
| CHANGELOG untouched | always | house rules |
| Hot-path timing | before/after timing when touching list, status, session output, startup, tmux layer | PR template checklist |
| gofmt + go vet | before every push | lint CI fails on formatting |

## Layer 4 — verdict protocol (how the loop behaves)

- **SHA-bound:** every verdict binds to one exact head SHA; any push voids it and
  triggers re-review. Batch fixes into one push.
- **Rank-up:** `needs-work` always names 1–3 concrete upgrading actions. Doing
  exactly those and saying so is the fastest path to `good`.
- **Silence-only close:** nothing closes on content; only ~2 weeks of author
  silence on a `needs-*` PR closes it, and reopening is always available. Any
  reply resets the clock.
- **Misfire path:** the gate is biased toward false-clean; if it wrongs a
  good-faith PR, say so with evidence — humans override (`bad-gate`) and the
  heuristic gets tuned.
- **Merge:** two independent AI reviews clean AND CI green AND the human
  maintainer lands it. No bot merges. Ever.
