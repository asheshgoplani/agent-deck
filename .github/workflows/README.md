# GitHub Actions workflows

This directory holds the CI/CD gates and automation for agent-deck. After the
v1.7.42 audit (#682), every workflow here is either (a) green on main, (b)
green when its trigger path fires, or (c) explicitly alert-only off the PR
path. No PR should merge with a red check unless the failure is a real,
actionable regression.

## Active PR gates (block merge)

These run on pull requests and **must go green** before merge.

| Workflow | Trigger | What it gates |
|---|---|---|
| `session-persistence.yml` | PR touching tmux/session lifecycle paths, or `workflow_dispatch` | The eight `TestPersistence_*` tests (race-detector on) plus `scripts/verify-session-persistence.sh` end-to-end. Covers the class of bug where a single SSH logout destroys every managed tmux session on Linux+systemd. See the "Session persistence: mandatory test coverage" section in the root `CLAUDE.md`. |
| `lighthouse-ci.yml` | PR touching `internal/web/**`, `.lighthouserc.json`, `tests/lighthouse/**`, or the workflow itself; also re-runs on `labeled` / `unlabeled` so the override below is reactive | Two-layer Lighthouse gate against `agent-deck web --no-tui`: (1) absolute thresholds in `.lighthouserc.json` (`total-byte-weight`, `resource-summary:script:size`, `cumulative-layout-shift` as hard error; FCP/LCP/TBT/Speed Index as soft warn); (2) bundle-delta gate (`tests/lighthouse/compare-deltas.mjs`) that fails if a single PR grows `total-byte-weight` or `script:size` by more than 5% vs the base ref. Reinstated in v1.7.70 after the `--no-tui` flag fixed the bubbletea/headless-CI start failure that disabled the gate in v1.7.42. **Maintainer override on the delta gate**: apply the `lighthouse-regression-acknowledged` label (auto-created by the workflow) to acknowledge an intentional regression — the workflow re-runs on the `labeled` event and the check turns green. The absolute thresholds in layer (1) do not participate in the override. |

Any other red on a PR is either a pre-release workflow (see below) or a bug —
file an issue and fix it, don't merge through it.

## Release automation (tag-triggered)

| Workflow | Trigger | What it does |
|---|---|---|
| `release.yml` | push tag `v*` | Validates the tag matches `cmd/agent-deck/main.go`'s `Version`, runs `go test -race ./...`, runs `goreleaser --clean` to build Darwin/Linux × amd64/arm64 tarballs into a **draft** release, asserts the four tarballs + `checksums.txt` landed, verifies every tarball against its published SHA-256, signs SLSA build provenance — and only then publishes the release. Finally it pushes the Homebrew formula, best-effort. Replaces the pre-#332 manual `make release-local` step. |
| `homebrew-verify.yml` | `workflow_run` after `release.yml` completes successfully (plus weekly cron, `workflow_dispatch`, and PRs touching install docs) | Probes the live tap: formula exists, its version matches `/releases/latest`, and every URL in it resolves. Moved off the tag-push trigger in #1759 because it used to race the release it was verifying. |

### The release is published last, and only once (#1759)

Assets are attached to a **draft** (`release.draft: true` in `.goreleaser.yml`) and
`release.yml`'s `Publish release` step is the only thing that ever makes a
release visible — after asset presence and checksum verification pass. If any
step fails, the draft is never published, so `agent-deck update`, `brew`, and
`agent-deck remote update` keep resolving the previous good release instead of
an empty one. A leftover draft is adopted by the next run of the workflow for
that tag (`release.use_existing_draft: true`).

**Cutting a release therefore means pushing the tag — nothing else.** Never
create the GitHub release yourself, and never with `gh release create` without
`--draft`: GoReleaser copies the draft flag of a release that already exists, so
a pre-published release stays public and asset-less for the whole ~11-minute
build (exactly what broke v1.10.10 and v1.10.11). `release.yml` now forces such
a release back to draft before building, but that safety net exists to catch
mistakes, not to be relied on.

### Provenance is signed before publishing; the tap is pushed after (#1760, #1763)

The tail of `release.yml` is ordered deliberately:

1. **Attest build provenance** — signs the verified artifacts. It runs *before*
   publishing so provenance is a precondition of a release being visible. If
   signing fails, the draft is never published and nobody is handed an
   artifact that `gh attestation verify` cannot check (SECURITY.md advertises
   exactly that command).
2. **Publish release** — the one place a release becomes visible.
3. **Update Homebrew tap formula** — `continue-on-error: true`. Best effort, and
   after publishing because the formula's download URLs only resolve once the
   release is public.

GoReleaser used to push the tap itself, in the middle of `goreleaser release`.
That put a third-party package manager in the release's critical path: when
`HOMEBREW_TAP_GITHUB_TOKEN` started returning `401 Bad credentials`, the
GoReleaser step failed and **every step after it was skipped**, so v1.10.9,
v1.10.10 and v1.10.11 shipped with no SLSA provenance at all (#1760). Now
`brews.skip_upload: true` in `.goreleaser.yml` makes GoReleaser stop at
generating `dist/homebrew/Formula/agent-deck.rb`, and the workflow owns the push.

The tap step cannot fail a release, but it is not silent either: it emits a
`::warning::` annotation plus a job summary with recovery commands, and it
refuses to push unless the generated formula's version matches the tag and all
four of its `sha256` values match the release's verified `checksums.txt` — a
formula with a stale checksum breaks `brew install` for everyone. A red-but-
ignored tap step on a green release means the tap needs attention (usually the
token), not that the release is bad.

| `pages.yml` | push to `main` touching `site/**`, or `workflow_dispatch` | Deploys the static landing site under `site/` to GitHub Pages. |

## Notification-only (no gate, no build)

| Workflow | Trigger | What it does |
|---|---|---|
| `issue-notify.yml` | issue opened | Posts issue context (title, body, labels, related issues, recent commits) to the configured ntfy topic so the conductor picks it up. |
| `pr-notify.yml` | PR opened or marked ready-for-review | Posts PR context (files, commits, reviews, comments) to the same ntfy topic. |

Both expect `secrets.NTFY_TOPIC` to be set on the repo. Neither blocks
anything — they can fail silently without affecting merges.

## Schedule-only (alert-only, not a PR gate)

| Workflow | Trigger | What it does |
|---|---|---|
| `weekly-regression.yml` | Sunday 00:00 UTC cron, or `workflow_dispatch` | Runs the Playwright visual-regression suite (`tests/e2e/pw-visual-regression.config.ts`) and Lighthouse CI (`.lighthouserc.json`) against a freshly built `agent-deck web` server. On failure, opens or appends to a single `Weekly regression check: … [date]` issue labelled `regression,automated` (idempotent — no duplicate issues on back-to-back failures). **Alert-only** — does not block any PR. |

> **Note on the v1.7.70 fix:** the bubbletea cancel-reader failure that broke
> `agent-deck web` on headless CI (`error creating cancelreader: bubbletea:
> error creating cancel reader: add reader to epoll interest list`) is fixed
> by the `--no-tui` flag added in v1.7.70. Both `lighthouse-ci.yml` (PR-time)
> and `weekly-regression.yml` now invoke the binary with `--no-tui`, which
> skips the TUI init while keeping the HTTP server. The Playwright
> visual-regression step in `weekly-regression.yml` benefits from the same
> flag — the test server now binds reliably on every Sunday run.

## Deliberately removed in v1.7.42 (#682), partially reinstated in v1.7.70

In v1.7.42 these PR gates were deleted because they were red on every run and
were teaching the team to ignore red checks. The shared root cause — the
bubbletea cancel-reader failing on headless CI — was fixed in v1.7.70 by the
`--no-tui` flag on `agent-deck web`. The Lighthouse gate has been reinstated;
visual-regression is still pending its own re-baseline of screenshot
fixtures.

| Workflow | Status |
|---|---|
| `lighthouse-ci.yml` | **Reinstated in v1.7.70.** Listed under "Active PR gates" above. Thresholds re-baselined against the current webui bundle (`./tests/lighthouse/calibrate.sh` output) and the `--no-tui` flag is wired through `.lighthouserc.json`, `tests/lighthouse/*.sh`, and `weekly-regression.yml`. |
| `visual-regression.yml` | **Still removed.** The server-start issue is fixed by `--no-tui`, but the screenshot baselines under `tests/e2e/visual/__screenshots__/` were never re-baselined and the suite still flakes on shared runners. The same matrix continues to run weekly via `weekly-regression.yml` (now reliably, since `--no-tui` lets the server bind). |

Reach the repo at commits before v1.7.42 (`a4b7079^`) if you need the
original `visual-regression.yml` file. To reinstate it: re-baseline the
screenshots with `cd tests/e2e && npx playwright test --config=pw-visual-regression.config.ts --update-snapshots`,
then bring the workflow file back in the same PR.

## Adding a new workflow

1. The gate must be green on the first merged run. If you can't guarantee
   that, make it `workflow_dispatch`-only or `continue-on-error: true` until
   it is.
2. Add a row in this README.
3. Reference the CLAUDE.md section in the project root if the gate is
   mandatory (see `session-persistence.yml` for the pattern).
