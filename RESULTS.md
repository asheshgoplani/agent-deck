# PR #2018 verification results

## Rebase evidence

- Pre-rebase head: `2f285923b52391b27915a923280c42ceb567d442`.
- Fresh `origin/main`: `7771aca6169e93973851d8b85fe2238b4b3897be`.
- The seven commits were rebased before findings work. Post-rebase head was
  `68e6a4eae9acb7959e22e1cfdfc9668ca5699d2e` and was force-pushed with an
  exact `--force-with-lease` against the recorded old head.
- `git range-diff` matched all seven old/new commits. The only non-identical
  entry was the expected add/add resolution for this repository-level
  `RESULTS.md`; all production, test, documentation, and skill patches replayed.

## Findings addressed

The complete conversation and inline-review history was read through both
`gh pr view 2018 --comments` and the pull-request review-comment API.

- Earlier sixgate findings remain fixed: verdict checks re-read current G1-G5
  evidence; gate roots and slugs cannot escape the repository; the historical
  context-inspector notes no longer claim absent evidence. Their regression
  suites pass.
- Earlier CI findings remain fixed: child environments use the repository
  sanitization helper, gosec/unused findings are clear, and panedrive resolves
  its state path through `agentpaths`.
- The slug-traversal inline finding is covered by the current containment and
  unsafe-path regression tests.
- The supposedly missing Claude model-switch golden is present at
  `internal/ctxinspect/ctxfixture/cases/claude-model-switch/expected-report.json`;
  the fixture suite passes.
- The Codex unmatched-AGENTS.md warning is already promoted to
  `Report.Caveats`, with a report-level regression assertion.
- Codex tag recognition already requires a lower-case XML-style name starter
  and a closing delimiter. Its enumeration includes digit-start, uppercase,
  empty, unterminated, and ordinary `<3 is a heart` inputs.
- Remaining defect fixed here: Gemini session totals now fall back per message
  when older records lack `tokens.total`, instead of dropping all old-format
  turns as soon as one newer record supplies a total.

## Revert proof

All Go execution was inside `golang:1.25` containers.

- Added `TestUpdateGeminiAnalyticsFromDisk_MixedFormatsFallbackPerMessage` with
  one old-format and one new-format Gemini message.
- RED before the fix: `TotalTokens() = 230, want 346 (per-message fallback)`.
- GREEN with the fix: the mixed-format test and the existing all-counters and
  reset-on-reparse sibling tests pass.
- Removed only the production per-message fallback after the green run and
  reran the regression. It returned the same RED `230, want 346`; the
  production hunk was then restored and reverified.

## Verification

- PASS: `go test ./internal/ctxinspect/codex ./internal/ctxinspect/ctxfixture ./internal/sixgate/... ./tools/sixgate -count=1`.
- PASS: focused Gemini accounting tests, including mixed formats, complete
  counters, and reset-on-reparse.
- PASS: `go build ./... && go vet ./...` in `golang:1.25`, after marking the
  bind-mounted checkout as a safe Git directory inside the disposable
  container so Go VCS stamping could inspect it.
- The broad `internal/session` package run reached one unrelated container-root
  assumption: `TestWriteJSONFileAtomic_SkipsUnchangedWrite` expects a chmod
  read-only directory to reject writes, while container root can still write.
  The affected Gemini family is green in that same image.

## Invariant check

- Accounting remains ordered and additive per Gemini message. A positive
  harness total is authoritative; absent or non-positive totals fail over to
  `input + output + thoughts + tool` for that message only.
- Cached tokens remain excluded from the total because they are a subset of
  input, preserving the no-double-count invariant.
- Reparse reset behavior, last-turn context selection, model selection, and
  cost counters are unchanged and covered by sibling tests.
- The touched parser is the only Gemini session-file accumulation path; the
  test deliberately enumerates both record generations in one session to
  prevent format siblings from diverging.

## CI state

The first complete post-fix run was GREEN on `4a638808`: all 16 reported checks
passed, including the eight-minute Full test suite PR gate, golangci,
govulncheck, CodeQL, eval smoke, persistence/race tests, snapshot tests,
performance tests, both harness platforms, diffscope, and intake. After this
CI-result paragraph was committed, the complete check set was required to
finish GREEN again on the exact final pushed head before handoff.
