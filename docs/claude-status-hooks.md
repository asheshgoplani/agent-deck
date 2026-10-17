# Claude status hooks

Claude can start a tool turn after a task notification or a blocked Stop without
emitting UserPromptSubmit. Agent Deck subscribes to PreToolUse so those tool turns
report running in session show, list, and Recall live status frames.

## Upgrade each host and Claude configuration

1. Upgrade the installed Agent Deck binary on the local or remote host. A local
   upgrade does not refresh a remote host's binary or settings.
2. Run `agent-deck hooks install` using that installed binary and the same
   `CLAUDE_CONFIG_DIR` as the Claude session. Repeat for additional configuration
   directories. Inspect `agent-deck hooks status` to verify the installed command.
3. Ensure Claude reloads the changed settings, restarting the Claude session when
   necessary. A process still using a previously loaded async PreToolUse entry
   does not gain this correction until it loads the new entry.

An upgraded notification daemon heals its existing hook installation at startup.
TUI startup also repairs a drifted existing installation. Development binaries do
not silently pin themselves into installed hook commands; explicit installation
uses the stable installed binary when available. Installation and removal preserve
user hooks, including user PreToolUse matchers.

## Ordering and cost

The installed PreToolUse entry is **synchronous**.
It sets `AGENTDECK_PRETOOL_SYNC=1` and runs only the status publication path before
returning. It makes no permission decision. It does not open profile databases,
scan Claude session metadata, reconcile names/cwd, index Recall, or spawn another
process. PostToolUse remains unsubscribed because the model can continue working
between tools.

Publishing before returning prevents a later PermissionRequest, Stop, or
SessionEnd from overtaking the tool's receiver. The handler also records its
receive time before reading input; under the publication lock it rejects an older
PreToolUse when a newer lifecycle event has already written status. Legacy async
PreToolUse commands lack the marker and are ignored, including commands first
scheduled after a newer permission event. Refreshing the installed settings is
therefore required; a receive timestamp alone cannot repair that schedule.

There is one handler process and a small synchronous file write per tool. Lock
contention is limited to 100 ms. If that budget expires, the handler leaves the
existing status intact and exits successfully so the tool can proceed. Filesystem
and process-start latency remain operating-system dependent. Event history stays
bounded to 200 entries. This is not a zero-latency or hard real-time guarantee.

A self-started thinking phase before its first tool has no PreToolUse event and
retains the prior lifecycle status until other evidence is available. Permission
requests still report waiting; Stop reports waiting and SessionEnd reports dead.
The existing two-minute hook fast path and completed-turn hook-lag correction are
unchanged. On a skipped publication, the existing pane fallback still applies
when the hook ages out. Codex and Gemini event mappings are unchanged.
