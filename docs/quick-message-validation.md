# Quick-message review validation

The composer-only branch is based on upstream main `8d0f1fe1` (2026-10-15).
It does not carry the nested-agent detection or alternate OpenCode launcher
implementation from #2456/#2516. Native queue availability uses main's configured
binary version probe. Send remains `o`, Skills remains `s`, and Queue is `Q`.
Inside the composer, Ctrl+E remains end-of-line; Ctrl+X E opens the editor.

## OpenCode 2 request and receipt

On 2026-10-11, the published [V2 OpenAPI specification](https://opencode.ai/v2/openapi.json)
and an installed `opencode v2.0.26` both accepted top-level `text` and `delivery`.
The request does not use a nested `prompt` object. The offline fixture asserts
exactly those two fields and rejects an unexpected shape.

The published specification was rechecked on 2026-10-15: `session.prompt`
still requires top-level `text` and permits top-level `delivery` and `resume`,
with `additionalProperties: false`. A nested `prompt` field is not accepted.

A temporary session was created for this real CLI request:

```sh
opencode2 api session.prompt \
  --param sessionID=ses_ed37c3a35ffeP6BUM5EtERigzE \
  --data '{"text":"agent-deck queue schema validation (no model execution)","delivery":"queue","resume":false}'
```

Captured reply:

```json
{"data":{"id":"msg_12c83d63c001nRp9Vemj04zRWY","sessionID":"ses_ed37c3a35ffeP6BUM5EtERigzE","time":{"created":1791748199997},"type":"user","payload":{"text":"agent-deck queue schema validation (no model execution)"},"delivery":"queue"}}
```

`resume: false` prevented model execution for this admission-only check. Production
omits that optional flag so OpenCode schedules execution normally. This confirms
the request shape and queue admission, not end-to-end consumption during a model
turn. The temporary session was deleted afterward.

## Checks

- Focused race tests pass in UI/session for composer, prompt hotkeys, keymap,
  footer, queue recipient continuity, request shape, receipt failures, and
  configured-version queue availability. The final run used disposable
  Bubblewrap container isolation with Go 1.26.9:

  ```text
  go test -race ./internal/session ./internal/ui \
    -run 'Test(QueueOpenCodePrompt|QuickMessage|SupportsOpenCodeQueue|PromptInput|Hotkey|HelpBar)' -count=1
  ok .../internal/session 1.113s
  ok .../internal/ui      1.625s
  ```

  Initial container attempts lacked a usable `/bin/sh`; the container's shell
  symlink was corrected to its resolved Nix-store path before this passing run.
- `go build ./...`, `go vet ./...`, `make lint` (golangci-lint v2.14.0),
  gofmt, and `git diff --check` pass. The editor's intentional user-controlled
  shell command has a documented G204/G702 trust-boundary annotation; neither
  draft text nor the generated filename is interpolated as executable content.
- Full container-isolated UI race runs now complete on both unchanged upstream
  `8d0f1fe1` and this PR, using identical dependencies and configuration:

  ```text
  go test -race ./internal/ui -timeout 12m -count=1 -json
  main: 216.715s, FAIL
  PR:   214.916s, FAIL
  PR-only failures: []
  Main-only failures: []
  Race reports: none
  Timeouts: none
  ```

  Both runs fail exactly the same seven top-level tests (and the same subtests):
  `TestDialogFitFramesGolden`, `TestRemoteLegacyDialog_Golden`,
  `TestRemoteSwitch_ShiftPOpensRemoteEditDialog`,
  `TestIssue2388_ModelChangeDropsUnsupportedEffort`,
  `TestIssue2388_DialogUsesProbedCodexCatalog`,
  `TestNewDialog_DarkThemeViewGolden_Issue2449`, and
  `TestNewSessionFlow_Golden`. These are confirmed pre-existing failures, not
  composer regressions. The three review-mentioned goldens differ in the Cursor
  label (`cursor agent` versus `agent`), not an `opencode2` row. No unrelated
  golden files were regenerated.
- The complete PR UI suite passes with only those seven confirmed baseline
  failures excluded:

  ```text
  go test ./internal/ui -skip '^Test(DialogFitFramesGolden|RemoteLegacyDialog_Golden|RemoteSwitch_ShiftPOpensRemoteEditDialog|Issue2388_ModelChangeDropsUnsupportedEffort|Issue2388_DialogUsesProbedCodexCatalog|NewDialog_DarkThemeViewGolden_Issue2449|NewSessionFlow_Golden)$' -timeout 12m -count=1
  ok .../internal/ui 115.344s
  ```

- Earlier host UI runs were stopped after discovering the container-only test
  policy. Docker is unavailable (no daemon socket), so subsequent runs use
  disposable Bubblewrap containers with separate PID/IPC/mount namespaces,
  private HOME and /tmp, and no host tmux sockets or session data.
  An initial container run also lacked `/bin` in PATH, a valid SHELL, and usable
  fixture utilities; it produced integration failures and exhausted a four-minute
  package budget. Supplying Bash/coreutils/git/sed/grep and correcting PATH/SHELL
  resolves all those additional integration failures on **both** revisions:

  ```text
  go test -race ./internal/ui -run 'Test(EmbeddedRemoteAttach_ToleratesConfigErrorAndLeadingOutput|EmbeddedMoshCloseQuitsGracefully|EmbeddedTerminal_HonoursTmuxOptionOverrides|Issue1595CaptureVisiblePaneDedicatedSocket|Issue2524_|Issue2531_|BackgroundStatusPassOwnershipLinear|TitleFormatWebAndTUIGroupPersistence)' -timeout 3m -count=1
  PR:   ok .../internal/ui 39.160s
  main: ok .../internal/ui 39.112s
  ```

  `TestUpdateBannerWidthSweep` also passes separately on both revisions
  (PR: 5.348s; main: 5.345s). Its presence in the earlier timeout stack was not
  evidence of a failing or hanging test; the four-minute package budget expired
  after earlier tests had consumed it. No production fix is needed for these
  container-setup failures.
- The remote composer remains explicitly unsupported and its transport test
  skipped; an executable regression prevents remote rows targeting local input.
- The full repository test suite was not run for this revision. Durable queue
  integration for other harnesses remains the maintainer's suggested follow-up.
