# Regression proof — would this unit have caught the blank percentage?

The claim under test is not "the gate passes". A gate that passes proves nothing;
a gate that cannot fail is decoration. The claim is:

> **Driver A + the G2 assert runner, alone, would have caught the blank
> percentage before anybody opened the feature.**

A claim like that is only worth anything if it was executed. So it was: the bug
was put back into the shipping renderer, the journey was driven against it, and
the failing artifacts are in this directory. Then it was reverted and the passing
artifacts one level up were re-recorded.

## What was reintroduced

`reintroduced-bug.diff` — one hunk in `internal/ui/context_pager.go`,
`contextGaugeLine`. The percentage is still computed and then not printed, which
is exactly the shape the feature shipped in:

```
-	return fmt.Sprintf("%s  %s / %s  (%.1f%%)  fixed startup overhead",
-		contextGaugeBar(pct, contextGaugeWidth), amount, contextTokenAmount(rep.Window.Tokens), pct)
+	return fmt.Sprintf("%s  %s / %s  ( %%)  fixed startup overhead",
+		contextGaugeBar(pct, contextGaugeWidth), amount, contextTokenAmount(rep.Window.Tokens))
```

`04-overview.screen.txt` is the frame the driver recorded against that build. Line 5:

```
  [█░░░░░░░░░░░░░░░░░░░░░░░░░░░]  27.0k / 1.0M  ( %)  fixed startup overhead
```

That is the literal thing Ashesh saw when he opened the feature.

## What the gates did

| gate | outcome | why |
|------|---------|-----|
| **G1 DRIVE** | **PASS** | Correct, and important. A drive records what happened; it does not judge. Every step ran, every capture produced a frame, no PTY was created. A driver that failed here would be reporting on itself, not on the product. |
| **G2 ASSERT** | **FAIL at `04-overview`** | 3 script assertions failed and the Blank Detector raised 6 findings. |

`results.md` is the failing report, verbatim. Its first line names the first
divergent step, which is the one fact a reader needs before any other.

### The three assertions that failed

- `04-overview` `screen_matches '\(\d{1,3}(?:\.\d)?%\)|context window size unknown, so no percentage is shown'`
  — the gauge stated neither a percentage nor the honest sentence explaining why
  there is none.
- `04-overview` `screen_not_matches '\(\s*%\s*\)'` — evidence quoted from line 5.
- `06-category-selected` `screen_not_matches '\(\s*%\s*\)'` — the miss was still
  on screen one keystroke later.

### The finding nobody wrote an assertion for

`09-back.screen.txt` carries no `( %)` assertion at all. The **Blank Detector**
flagged it anyway, twice (`blank-percent`, `orphan-percent`). That is the whole
argument for a blunt, always-on, no-opt-in lint over every frame of every gate:
the failure class this framework exists to catch is precisely the one nobody
knew to look for, so it cannot be left to whoever wrote the script that day.

### What the other two fixtures said

`claude-resumed` and `claude-unknown-version` **passed** during the same run.
Neither establishes a window size, so neither renders a percentage at all — they
take the honest "context window size unknown, so no percentage is shown" branch.
That is worth stating plainly: a single fixture would have made this proof a
coin flip. The bug lives on the anchored path, and only the anchored fixture sees
it. This is the argument for G3's matrix, in miniature, produced by accident.

## Reproducing it

```bash
# from a checkout at the commit these artifacts were recorded on
git apply docs/gates/context-inspector/G2-assert/regression-proof/reintroduced-bug.diff
go build -o /tmp/sixgate ./tools/sixgate
/tmp/sixgate drive  context-inspector -fixtures claude-cold   # PASS: it records
/tmp/sixgate assert context-inspector                          # FAIL at 04-overview
git checkout internal/ui/context_pager.go
/tmp/sixgate drive context-inspector && /tmp/sixgate assert context-inspector
```

Everything runs under the sandbox preamble
(`HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR=`).
No tmux server is spawned by any of it; the PTY census in `run.json` is the
evidence, not a promise.
