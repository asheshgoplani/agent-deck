# Conductor context reaching 1M — design

**Date:** 2026-08-07
**Status:** implemented.
**Follows:** `2026-07-27-conductor-context-budget-design.md`, which introduced
the delta heartbeat and the 120k/200k thresholds. Those landed; conductors
still reached 839k. This is why.

## Evidence

Two real conductor transcripts, attributed by content type.

`run-2026-08-06-baba-all-remaining` (target repo IgniTech/baba), peak **839k**,
never compacted:

| Bucket | chars | ~tokens | share |
| --- | ---: | ---: | ---: |
| Bash **command text** (414 calls) | 602k | 150k | 46% |
| Bash results (435) | 356k | 89k | 27% |
| Conductor's own prose | 169k | 42k | 13% |
| User turns + system reminders | 150k | 37k | 11% |

Growth was smooth — roughly 4k per turn over ~400 turns, no single spike. The
fat tail is in the command text, not the results: **113 Bash calls exceeded
1500 characters and together came to 434k characters (~108k tokens)**. Nearly
all of them are `cat > <role>-prompt.md <<'EOF'` here-docs writing a child
prompt. The templates are ~6k characters each and are near-identical across
tasks and rounds, so the conductor paid full price for the same boilerplate on
every implementer, every reviewer, and every fix round — and kept paying,
because a tool call never leaves the transcript.

`run-2026-07-29-ribbon-parity` (Adaptam/ui) has the opposite profile: tool
results are 71% of content, and **ten `Read` calls on PNG files account for
~1.1M characters (~275k tokens)** — more than every review in that run
combined. The conductor was opening screenshots to check its children's work.

## Root causes

1. **No self-signal.** `session children --json` reports `context_tokens` for
   every child and nothing for the parent. The 120k/200k thresholds from the
   previous design were therefore unenforceable: the one session nobody
   downstream rotates was also the one session with no number to watch. This
   is why a documented 200k hard ceiling produced an 839k conductor.
2. **Prompt bodies typed inline.** The skill presented every child prompt as a
   verbatim template to fill in and here-doc out, which put ~6k characters per
   launch into the transcript by construction.
3. **No rule against opening images.** Nothing forbade `Read`-ing a
   screenshot, and 30–40k tokens each is invisible until it isn't.

## Changes

**1. `parent_context_tokens` (agent-deck).** `session children` now reports
the parent's own context size from the same source as a child's — the newest
assistant turn's prompt size in its Claude transcript. Human output grows a
`self-ctx=NNNk` suffix on the header; JSON grows `parent_context_tokens`.

Omitted, never zeroed, when unresolvable (non-Claude parent, no assistant turn
yet): a supervisor thresholding on the field would read `0` as "context is
empty" and skip exactly the rotation the field exists to trigger. The
suffix/emit decision is `parentContextFields`, tested for both directions.

**2. `poll.sh` reports and shouts.** Every heartbeat line now ends in
`self=NNNk`. Crossing `SELF_SOFT` (120k) or `SELF_HARD` (200k) prints a banner
naming the action — flush and `/compact`, or hand off — and **repeats it every
beat** while over threshold, because a one-time warning scrolls away behind
four heartbeats. A build with no `parent_context_tokens` prints
`self=n/a (upgrade agent-deck)` rather than nothing: a missing signal must not
read as a low reading.

**3. `references/prompts/` + `render.sh`.** Every child prompt is now a
template file with `{{VAR}}` placeholders — `plan`, `impl`, `review-full`,
`review-incremental`, `fix`, plus two shared preambles. `render.sh` fills them:

```
render.sh <template> <out> KEY=value KEY@=path
```

`KEY@=path` substitutes a file's *contents*, which is what makes the expensive
paths free: a findings list goes verdict-file → findings-file → prompt without
re-entering the conductor's context, and a freeform spec goes
`gh issue view ... > spec-block.md` → prompt after a single read for the
injection check. Substitution is `str.replace`, not `re.sub`, so a findings
list containing `\1` or `&` lands verbatim. Unfilled placeholders are a
non-zero exit listing them — a child handed a prompt still containing
`{{BASE_BRANCH}}` improvises around it, and the run finds out a review round
later.

SKILL.md now carries a variable table instead of five prompt bodies, and the
inline `poll.sh` listing was replaced by a pointer to the copied file. Net:
1077 → 959 lines, and the ~108k of per-run here-doc cost drops to the varying
part only.

**4. "Never open an image."** A hard conductor rule, in both the rules list at
the top and the context-budget table. The implementer prompt gained a matching
obligation: describe in words what each screenshot shows, since the conductor
supervising it will never look. Extended to any binary or generated blob —
PDFs, bundles, minified JS, lockfiles. When a visual judgment genuinely must
be made, a subagent looks and reports in text.

Also corrected while in the table: `session output --tail 40` does not exist
(reported in three separate retros); the working form is `-q | tail -40`.

## Expected effect

On the baba run's shape: ~108k from here-docs and a large share of the 89k of
results become renders and file-to-file pipes; the self-signal makes the
existing 120k/200k rules fire instead of being advisory. On the Adaptam run's
shape, the image rule removes 275k outright. A conductor that still grows past
200k now hands off deliberately rather than discovering the ceiling.

## Not done

`--follow`'s heartbeat summary does not carry `parent_context_tokens`. The
skill's supervision loop is explicitly one-shot (`You never block`), so the
follow stream would be dead surface; add it if a non-orchestrate caller needs
it.
