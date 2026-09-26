# fix/never-prefer-empty-xdg-store-20260920 (round 2, review HOLD on 4e84dd1d)

- [x] 1. Root rule is marker-based, not count-based: `profiles/.active-root` (written by `migrate-paths`) decides when both roots are populated; no marker -> legacy + WARN; the only automatic protection is empty-beside-populated = stray -> populated root; matrix documented; post-migration `remove` + profile create keeps XDG (test + probe)
- [x] 2. `store_selected` / `stray_xdg_store` actually emitted: structured line after `logging.Init` (TUI, notify daemon), one stderr WARN per CLI process (not hook-handler/completion/doctor/health); tests
- [x] 3. Unreadable store = unknown, never 0: never loses to an empty store; prefer the readable populated root + WARN; doctor prints `unreadable`; test
- [x] 4. doctor counts rows in the clean layout too; test
- [x] 5. Remedies that work: stray WARN says "move the stray profiles/ aside"; refusal error names the real fix; `migrate-paths` writes the marker (plain and --force); end-to-end sandbox test legacy -> XDG then the CLI uses XDG
- [x] 6. `--group`/`--select` store open before the guards: note (kept before the no-TTY gate on purpose, #2011; the create guard makes it harmless)
- [x] CHANGELOG + config-reference + PR body; go build/vet/gofmt; incident reproductions re-run; CI; RESULTS.md

# core/recall-slice6-20260922

- [x] Establish baseline and failing-first tests on g14.
- [x] Implement typed timeline and same-transaction cursor.
- [x] Implement resumable follow with stale resync and under 2 s append response.
- [x] Add per-harness golden tests and documentation.
- [x] Verify build, vet, full affected suites, indexing performance and independent review.
- [x] Commit local branch and create bundle and results receipt.
- [x] Record wide blast radius and failing-first tests for profile, process, rotation, stats, lifecycle and runtime failures.
- [x] Fix the event bus, producer waits, CLI help and documentation.
- [x] Add explicit one-shot and watcher shutdown, stale-cursor and Close race proofs.
- [x] Compare committed-head full packages with origin/main on g14 and record every failure.
- [x] Review simplification, final host checks, RESULTS.md and verified local bundle.

# core/events-slice4-20260922 (round 3, local only)

- [x] Failing-first tests for profile routing, reserved status, batched tmux output, watcher profile and bounded shutdown.
- [x] Preserve the TUI's opened profile and batch output appends with a nonblocking producer path.
- [x] Run a read-only simplification review and correct its findings.
- [x] Compare tmux tap throughput with the same harness on the old and fixed heads.
- [x] Verify the real CLI's kill and resume path in a sandbox.
- [x] Finish full g14 package comparison, host checks, report and bundle.

# core/daemon-slice5-20260923 (round 3, local only)

- [x] Rebase slice 1 and slice 5 commits onto slice 4 r3, preserving both changelog entries.
- [x] Prove the eight-second bulk-restart timeout and each equivalence pair's socket call.
- [x] Fix the bulk-restart reply deadline and the optional stale-socket takeover findings.
- [x] Verify the equivalence test fails when daemon use is bypassed.
- [x] Run full touched-package tests on g14 and compare failures with origin/main.
- [x] Complete host checks, simplification review, RESULTS.md and bundle.

# tui-round5-r2-20260923 (local only)

- [x] Read the review and reproduce the four behavior gaps with focused assertions.
- [x] Fix digit navigation, split labels, search titles, jump hints, and switcher spacing.
- [x] Regenerate and inspect the affected G14 frames.
- [x] Rewrite into green commits and verify each commit on G14.
- [x] Record overlap hunks, results, and a verified branch bundle.
