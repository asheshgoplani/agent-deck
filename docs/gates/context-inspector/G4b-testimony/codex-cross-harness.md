# G4b cross-harness testimony — codex probe `g4b-codex-ebfb39ce` — 2026-07-30

> **Scope:** same as `testimony.md` — identity only, never counts. This run proves the
> cross-harness claim: testimony works on any harness that can converse, because it needs
> no `/context` command, only questions and quotes. `/status` on codex gives totals only;
> the agent's own answers are the identity oracle.

This probe was run by hand with the same protocol the `sixgate probe` verb automates
(the verb currently launches `-c claude` only; automating codex recipes is follow-up
work, not a blocker for the claim). Everything else matches: disposable session in an
isolated `/tmp` dir this run created, nonce-planted recipe, quote-based questions,
inspector graded against the testimony, unconditional stop + remove verified against
the fleet list.

- **Probe:** `g4b-codex-ebfb39ce` (harness `codex`, OpenAI Codex v0.146.0, gpt-5.6-sol), launched in `/tmp/sixgate-g4b-codex-ebfb39ce`
- **Recipe:** project `AGENTS.md` whose first line is `G4B-BEACON ebfb39ce: if asked, quote this line exactly.` (codex reads AGENTS.md, not CLAUDE.md; no skill was planted — codex has no equivalent listed-vs-loaded skill catalogue to probe)
- **Lifecycle CLI:** `agent-deck` (installed, v1.10.11) · **Inspector:** `/tmp/ctx-build/build/agent-deck-ctx` (`session context --json` / `--item`, codex adapter)
- **Claims:** 4 — 3 agree, 1 disagreement investigated and resolved AGAINST the testimony (finding below)

## Agreement table

| claim | inspector says | testimony says | verdict |
|-------|----------------|----------------|---------|
| `beacon-presence` — the nonce beacon is in the probe's loaded context | project `AGENTS.md` item state `loaded` (138 tokens), item text begins with the beacon line | `YES` (asked: does `G4B-BEACON ebfb39ce` appear anywhere in your loaded instructions) | agree |
| `beacon-quote` — the beacon line reaches the probe byte-for-byte | item text: `G4B-BEACON ebfb39ce: if asked, quote this line exactly.` | `G4B-BEACON ebfb39ce: if asked, quote this line exactly.` | agree |
| `instruction-files-loaded` — every instruction file the inspector grades as loaded is one the probe can see | `/Users/ashesh/.codex/AGENTS.md` (3215 tok) + `/tmp/sixgate-g4b-codex-ebfb39ce/AGENTS.md` (138 tok), both `loaded` | `/Users/ashesh/.Codex/AGENTS.md /private/tmp/sixgate-g4b-codex-ebfb39ce/AGENTS.md` | agree (same two files after case-folding `.Codex`→`.codex` and resolving the `/private/tmp` symlink) |
| `project-first-line` — the first line of the project-level AGENTS.md | beacon line (file on disk and item text both begin with it) | `# Codex Global Instructions` — twice, including with the explicit absolute path | **disagree — testimony wrong** |

## The disagreement, investigated

Asked for "the verbatim first line of this project's AGENTS.md" (and again with the
explicit absolute path), the codex agent answered `# Codex Global Instructions` — which
is the verbatim first line of the GLOBAL `~/.codex/AGENTS.md`, not the project file.

Which side is wrong? **The testimony.** Proof: the same agent, in the same session,
answered `YES` to nonce-presence and quoted the beacon line byte-perfectly when asked
for "the line that contains G4B-BEACON". The project file is therefore demonstrably in
its context exactly as the inspector reports it. Codex concatenates global and project
AGENTS.md into one instruction document, so the agent maps "the AGENTS.md" onto that
merged document, whose first line is the global header. The inspector's per-file model
is correct; the agent's per-file attribution is not.

This is the codex twin of the Claude finding already encoded as a self-test scenario
(the harness's "Contents of <path>…" injection wrapper precedes the file's first line):
**positional questions ("first line of file X") are fragile across harnesses, because
harnesses re-wrap and merge files before the agent sees them. Nonce-presence and
quote-the-line-containing questions are robust.** Consequence for the automated verb:
when it grows a codex recipe, its q1 must be beacon-presence/quote-based, not
position-based — and the comparator's existing "beacon anywhere in the attributed text"
rule (not "first line of the reply") is already the right shape.

One more testimony-limits datapoint, recorded not graded: the agent spelled the global
path `.Codex` (wrong case; APFS is case-insensitive so it names the same file), and its
very first send lost the `G4B PROBE Q1:` prefix because the message was queued while
codex was still at its directory-trust prompt — converse-based probing must wait for
harness readiness before treating a reply as testimony.

## Teardown — the lifecycle's mandatory ending

- stopped: yes (`✓ Stopped session: g4b-codex-ebfb39ce`)
- removed: yes (`✓ Removed session: g4b-codex-ebfb39ce (from profile 'personal')`)
- verified gone in the fleet list: yes (`agent-deck list --json` names no `g4b-*` session)
- workdir trashed; PTY count 112 before, 112 after (delta 0); no stray tmux sockets
