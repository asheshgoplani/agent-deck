# Lessons

- Allocate search row space to the full title before metadata, then let metadata use all remaining cells; a fixed fraction can truncate both a long title and a short distinguishing path.
- A switcher spacer must inspect the adjacent preview cell. Adding one at every width can erase the first letter of a section title.
- A shortcut label derived from a precomputed row number must use that same number for navigation; counting rendered headers includes duplicate view sections and points at the wrong row.
- Before changing a field during a multi-pass view rebuild, locate its final assignment. A later numbering pass can silently overwrite an earlier duplicate-row fix.
- For narrow preview errors, inspect the rendered frame: a subtitle helper may truncate text that fits as a body line. Keep the actionable reason in the first visible body row.
- When a requested behavior reverses an existing contract, update the old assertion alongside the new regression test; a passing new test does not make the old expectation disappear.
- A passing regenerated visual check only proves consistency with its new golden. Inspect the changed pixels or text for the original symptom before accepting the golden.
- Centering an overlay does not make an overheight dialog fit. Bound its inner rows before framing, and verify the title, focused control, and footer in the physical terminal capture.
- A dialog's own centering tests can pass while the live screen stays anchored if Home never passes terminal dimensions. Verify the actual key route and size wiring.
- A framed dialog that exactly fills terminal rows can still scroll its top border away when the renderer emits a trailing newline. Leave one row of headroom and assert both borders in the captured terminal frame.

- When a test checks an operator-facing recovery message, assert the required paths and instruction independently. Exact adjacent wording can fail a correct message without testing its meaning.
- A stream-idle test must not wait for a slower durability sync between messages. After batching, `events.Flush` can wait for the one-second sync tick and close a deliberately short-idle subscription even when events stream promptly.

- Delayed subprocess regression fixtures must register gate-release cleanup before waiting for entry and explicitly join every worker after a timeout. Acquiring the worker's mutex does not prove that worker has run.
- Docker-mounted Git worktrees need accessible repository metadata or `-buildvcs=false` for nested fixture builds. Expected-red checks must match the specific behavioral assertion, not merely the test name.
- Compare total observable subprocess calls across implementations. Protocol-specific delays establish TTL stress, not a representative speedup ratio.
- A tmux fake must distinguish `pane_pid` from `pane_dead` queries. Returning PID 1 for a liveness query reports the pane as dead and prevents the intended polling path.
- Construct tmux fixtures with the real tool command so status detection loads the correct busy patterns. Assert exact baseline call counts to detect accidental bypass of the metadata path.
- Hosted-runner checkout ownership may differ from container UID 1000. Make only required module metadata writable when using `-mod=mod`.
- Do not infer "hook X never fires" from the absence of a transcript record type; check the artifact the hook itself writes (the hook status file's ts against the turn's end time). Rebuild a commit rather than following it with a "remove binary" commit when a build artifact lands in it: history, not the tip, is what gets pushed.
- When adding a pane capture to a shared helper, list every caller first: a helper shared by a CLI poll and a hook handler puts the capture on the hook edge too. Hook handlers never capture panes; make the capture opt-in at the call site. Order samples from several processes at the resolution they are taken at (milliseconds), not the resolution they are displayed at.
- A wrapper that runs the user's own command (statusLine, hook) must fail open on every path of its own, including the "resolve where to store" step that runs before the command: a shared helper that calls os.Exit is the wrong shape for it. Wire-time validation belongs where the wiring is written (refuse a slot the store cannot name) so the failure never reaches the user's render path.
- A wrap function's line limit is the limit of the line the piece lands on, not the current line: check `lead+piece` against the continuation width after a flush, and prove the result with a second pass (each output line must come back unchanged).
- `go test` on the host is banned for this repo even for a "pure" package: an unset path option resolved through agentpaths touches the real ~/.cache. Every test run goes through the Docker image; every path an option can default to gets an explicit temp value in tests.
- Two-step export in ad-hoc probe shells too, not only in scripts: `export HOME=$T; export XDG_DATA_HOME=$HOME/...` on separate lines. A one-line `export HOME=$T XDG_DATA_HOME=$HOME/.local/share` expands the real `$HOME` and points the probe at the real data dir (the exact shape of the 2026-09-19/20 stray store). Under zsh use `unsetopt multios` before `2>&1 >/dev/null`, or the stdout/stderr split lies.
- A read transaction over an index does not freeze its source files. When a client cursor is derived from a native transcript, validate the physical file across parsing and retry a replacement or rewrite; a pure append can keep the old prefix cursor valid.
- A few selected event assertions are not a golden timeline. Pin the complete canonical event list for each harness, including metadata, unknown raw payloads and source order, so dropped or reordered records fail the test.
