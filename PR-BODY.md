## Summary

Issue #2079 names two call sites where a truncated `launch --message-file`
paste can read as a clean delivery: `sendMessageWhenReady`
(`internal/session/instance.go`) and the `launch --no-wait` post-send
verifier, `pollPromptConsumed` (`cmd/agent-deck/launch_verify_prompt.go`).
Round 1 of this branch fixed only the first. This round covers the second.

- Claude's composer collapses a framed multi-line paste behind a
  `[Pasted text #N +M lines]` marker whether the paste arrived whole or was
  cut short. `pollPromptConsumed` now checks the declared `M` against the
  message's real line count once the composer looks consumed, the same
  primitives (`send.ExpectedPasteMarkerLines`, `send.PasteMarkerLineCounts`)
  round 1 added for the first call site.
- A marker declaring fewer lines than the message is reported as
  `"prompt truncated in transit"` instead of success, and the one retry is
  skipped (retyping onto an already-submitted fragment risks a duplicate
  submission).
- A consumed-looking composer with no marker at all, for a message that
  expects one, is reported as unknown — never as success.
- A marker that declares at least as many lines as the message (or a
  single-line message, which never collapses behind a marker) is unchanged:
  a silent success.

## Test plan

- [x] New failing-first test file
      `cmd/agent-deck/issue2079_launch_verify_prompt_test.go`, using the
      existing `mockSendRetryTarget` fake.
- [x] Revert-proof in `golang:1.25`: production check disabled → both new
      tests fail with the expected messages; check restored → all pass
      alongside the full pre-existing `TestVerifyPromptConsumedAfterLaunch_*`
      suite.
- [x] `go build ./...` and `go vet ./...`: PASS (host).
- [x] `go test ./cmd/agent-deck/...` and `./internal/send/...`: PASS
      (`golang:1.25`).

See `RESULTS.md` for full details and caveats.
