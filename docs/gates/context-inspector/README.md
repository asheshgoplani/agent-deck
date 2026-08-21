# Reading this directory

This is the first feature to go through all six SIXGATE gates. `VERDICT.md` is generated
and byte-checked, so it cannot carry commentary — this file is the commentary.

The rule is **no transcript, not done**, and the reason is on the record: the context
inspector was declared done on code analysis, a 2,961-transcript corpus replay and an
adversarial review, and the first thing its user saw when they opened it was a blank
percentage they had to ask about. Everything here exists to make that impossible to
repeat quietly.

```sh
./build/sixgate verdict context-inspector --check   # exit 0 == the evidence still stands
```

## Where to look first

| If you want to know | Open |
|---|---|
| what a human was supposed to do | [`G0-script.yaml`](G0-script.yaml) |
| what a human actually saw | [`G1-drive/pane-claude-cold/transcript.md`](G1-drive/pane-claude-cold/) — the **shipped binary**, in a real terminal |
| whether the screen held up | [`G2-assert/results.md`](G2-assert/results.md) |
| whether it holds up in worlds nobody demoed | [`G3-matrix/matrix.md`](G3-matrix/matrix.md) |
| whether the numbers are true | [`G4-oracle/parity.md`](G4-oracle/parity.md) |
| what it feels like to arrive | [`G5-coldeye/report.md`](G5-coldeye/report.md) |

## The one line that matters

The frame that shipped with a blank percentage now reads, on a captured terminal:

```
[█░░░░░░░░░░░░░░░░░░░░░░░░░░░]  27.0k / 1.0M  (2.7%)  fixed startup overhead
```

and G0 asserts `screen_not_matches: '\(\s*%\s*\)'` — the literal thing the user saw —
on every frame of the journey, at every terminal size in the matrix, permanently.

## What each gate found that reading the source had not

This is the honest measure of whether the framework earned its keep. None of the
following came from analysis; each came from a recording.

- **G1** corrected the script three times. The drill into memory files is an *Overview*
  journey, not a *Breakdown* one — Breakdown is a flat ranking whose breadcrumb never
  names a category. The separator is a chevron. And the harness itself was rendering
  "no sessions" above a populated list.
- **Driver B** corrected the cold first run: a machine that has never run agent-deck
  shows a setup wizard dismissed with `Esc`, not the hooks modal dismissed with `n`.
  The A-vs-B seam report then found that the memory-file token count tracks the
  project's absolute path length.
- **G3** found that every Codex row had been silently exercising the no-rollout path,
  because the fixture never placed the rollout where Codex's resolver looks.
- **G4** is the gate with teeth here. Three figures are compared against the provider's
  own usage accounting and agree — the anchor to the token (27008 = 27008), the cache
  share to the token (3000 = 3000), and the gauge inside its printed rounding
  (27000 vs 27008, ±50). Six figures have **no** oracle, because collecting the real one
  means typing `/context` into a live, billed session and this host holds the
  maintainer's live fleet. Those six therefore cost a permanent on-screen estimate
  marker, and G2 asserts all twelve of them (six figures × two drivers) against the
  recorded frames. Strip `RECON/~est` from one row of one frame and G2 fails at exactly
  that step — which is how this was verified rather than asserted.
- **G5 found the most uncomfortable thing in this directory.** A reviewer who had never
  seen the software was given the binary, one sentence, and a computer with a Claude
  session already on it. In three minutes they never pressed `C`. They never saw the
  context inspector at all. Their four findings are about the deck the pager lives
  behind — a session that simultaneously reported `error`, `● Connected` and `No tmux
  session running`; an unlabelled `882.4G/926.4G`; a help heading naming an old version;
  a preview toggle that changes only its own label. Every one is real, none belongs to
  this feature's files, and all four are accepted in writing in
  [`resolutions.yaml`](G5-coldeye/resolutions.yaml) rather than quietly dropped.

  **The finding is the absence.** G1, G2, G3 and G4 all press `C`, because the script
  tells them to. G5 is the only gate that does not know to, and it says plainly that
  this feature is not reachable in three minutes by somebody who does not already know
  it exists. No amount of source reading produces that sentence.

## What this verdict does NOT claim

Stated here rather than left to be discovered:

- **The strong oracle was not collected.** Claude Code's own `/context` panel is the
  real source of truth for six of the nine figures and it is not in this directory. The
  artifact says so on every affected row, and the price is the must-label contract.
- **The collected oracle is `independent-extraction`, not `independent-authorship`.**
  The provider wrote the numbers, but the file lives in this repository's recorded
  corpus, so a mistake baked into the corpus would be invisible to the comparison. It
  proves the arithmetic and the rounding, not the field semantics.
- **G5 reviewed the product's arrival, not this feature's screen.** See above.
- **The evidence covers the frames recorded on 2026-07-28 at the commit in
  `VERDICT.json`.** `--check` re-hashes every source in `owns`; the moment one changes,
  this stops counting as evidence and the gate re-opens. That is the intended
  behaviour, not a limitation.
