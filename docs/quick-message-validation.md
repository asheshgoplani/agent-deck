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
- Full `go test ./internal/ui` runs on this branch and unchanged upstream
  `8d0f1fe1` were interrupted after discovering the current contributor policy
  requires container-only tests. The initial runs used disposable HOME and
  private TMUX_TMPDIR directories but were not container-isolated. Both runs
  had already reported the following failures:
  `TestDialogFitFramesGolden`, `TestRemoteLegacyDialog_Golden`,
  `TestRemoteSwitch_ShiftPOpensRemoteEditDialog`,
  `TestIssue2388_ModelChangeDropsUnsupportedEffort`,
  `TestIssue2388_DialogUsesProbedCodexCatalog`,
  `TestNewDialog_DarkThemeViewGolden_Issue2449`, and
  `TestNewSessionFlow_Golden`. The three review-mentioned goldens now differ
  in the Cursor label (`cursor agent` versus `agent`), not an `opencode2` row.
  No unrelated golden files were regenerated. These interrupted runs do not
  establish the complete suite outcome. Docker is unavailable on this host
  (no daemon socket); further tests use a disposable Bubblewrap container with
  separate PID/IPC/mount namespaces, private HOME and /tmp, and no host tmux
  sockets or session data.
- An additional container-isolated run of all seven named tests on unchanged
  `8d0f1fe1` reproduces every failure, including the three review-mentioned
  goldens' Cursor-label mismatches.
- The full container-isolated `go test -race ./internal/ui -timeout 4m -count=1`
  did not pass: it reported those failures plus embedded-terminal, dedicated
  socket, watcher/health-alert, background-status ownership, and group-persistence
  integration failures, then reached the four-minute package timeout while
  `TestUpdateBannerWidthSweep` was running. The additional failures and timeout
  have not been baseline-classified; no claim is made that all failures are
  pre-existing or that the full UI suite passes in this container.
- The remote composer remains explicitly unsupported and its transport test
  skipped; an executable regression prevents remote rows targeting local input.
- The full repository test suite was not run for this revision. Durable queue
  integration for other harnesses remains the maintainer's suggested follow-up.
