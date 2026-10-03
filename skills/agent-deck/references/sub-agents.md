# Sub-Agents, Consultations, and Peer Sessions

Launching child sessions, worker prompt conventions, the completion sentinel, consulting Codex or Gemini, and when to create a root-level peer instead.

## Sub-Agent Launch

**Use when:** User says "launch sub-agent", "create sub-agent", "spawn agent"

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" [--mcp name] [--wait]
```

The script auto-detects current session/profile and creates a child session.

### Retrieval Modes

| Mode | Command | Use When |
|------|---------|----------|
| **Fire & forget** | (no --wait) | Default. Tell user: "Ask me to check when ready" |
| **On-demand** | `agent-deck session output "Title"` | User asks to check |
| **Blocking** | `--wait` flag | Need immediate result |

### Fanning out several children?

This section covers **one** child (launch + one of the three retrieval modes). For a *fleet* — several children in parallel, supervised non-blockingly from the parent — load the sibling [fleet skill](../../fleet/SKILL.md) instead. It covers parented fan-out, polling live status and asserted completions via `agent-deck session children --json` (plus the push variant `--follow --until-done`), answering children stuck in `waiting`, and the grouping/`--parent` pitfalls.

### Recommended MCPs

| Task Type | MCPs |
|-----------|------|
| Web research | `exa`, `firecrawl` |
| Code documentation | `context7` |
| Complex reasoning | `sequential-thinking` |

### Worker Prompt Conventions

When the prompt asks the worker to `Edit` or `Write` any **existing** file, include an explicit **prelude-read** as the first step. Claude Code's tool guard rejects `Edit`/`Write` before `Read` of the same path, and conductor-spawned workers hit this mid-task (#968) when their prompt jumps straight into the change. The interruption forces the worker to backfill reads inside its main loop, breaking flow and burning cycles.

**Template skeleton — bake this into every worker prompt that mutates code:**

```
## Step 0 — Prelude reads
Read every file you intend to Edit/Write below. Read calls are cheap
and do NOT count against scope discipline; they prevent tool-guard
interruptions mid-task. Skip only for paths that will be created fresh.

Files to read first:
  - <path/to/file/you/will/edit>
  - <path/to/other/file/you/will/edit>

## Step 1 — Investigation
…

## Step 2 — Implementation
…
```

**Rules:**
- Any file the worker will modify: prelude `Read` it first, even if the worker "knows" the contents.
- Brand-new files (`Write` to a path that does not yet exist): no prelude needed — the guard only applies to modifications.
- For conductor nudges that re-fire the same prompt across cycles, keep Step 0 in the prompt. The second-cycle process may not have prior reads in context.

### Completion sentinel (trustworthy "worker finished" signal, #1186)

A worker's `Stop` hook fires at the end of *every* turn, so "waiting" never means "done" — the conductor would otherwise have to poll RESULTS files / `gh pr` / `session output` to know a task actually finished. Instead, instruct every worker to **assert completion** by ending its final turn with a single machine-greppable line:

```
===AGENTDECK_DONE=== status=<ok|fail> summary=<one line to end of line>
```

agent-deck detects this on the `Stop` edge (scanning the transcript tail) and emits a distinct event to the parent:

```
[DONE] Child '<name>' (<id>) finished: status=ok summary=<...>
```

instead of the generic `[EVENT] … is waiting`. This is by-construction: completion is asserted by the only party that knows (the worker), not inferred from terminal cosmetics.

**Bake this line into every worker prompt's final instruction:**

```
## Final step — assert completion
When the task is fully done, print exactly this as the last line of your final message:
  ===AGENTDECK_DONE=== status=ok summary=<what you accomplished, one line>
Use status=fail if you could not complete it; put the blocker in the summary.
```

Notes:
- Fires once per distinct completion (idempotent per task): re-reading the same sentinel across polls — or repeating an identical sentinel on a later `Stop` — does not re-emit. A genuinely new completion (different summary) emits again.
- Absent on ordinary mid-task `Stop` edges, so existing `waiting` behavior is unchanged.
- Malformed sentinel lines (missing/invalid `status`) are ignored, not guessed at.

## Consult Another Agent (Codex, Gemini)

**Use when:** User says "consult with codex", "ask gemini", "get codex's opinion", "what does codex think", "consult another agent", "brainstorm with codex/gemini", "get a second opinion"

**IMPORTANT:** You MUST use the `--tool` flag to specify which agent. Without it, the script defaults to Claude.

### Quick Reference

```bash
# Consult Codex (MUST include --tool codex)
$SKILL_DIR/scripts/launch-subagent.sh "Consult Codex" "Your question here" --tool codex --wait --timeout 120

# Consult Gemini (MUST include --tool gemini)
$SKILL_DIR/scripts/launch-subagent.sh "Consult Gemini" "Your question here" --tool gemini --wait --timeout 120
```

**DO NOT** try to create Codex/Gemini sessions manually with `agent-deck add`. Always use the script above. It handles tool-specific initialization, readiness detection, and output retrieval automatically.

### Full Options

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" \
  --tool codex|gemini \     # REQUIRED for non-Claude agents
  --path /project/dir \     # Working directory (auto-inherits parent path if omitted)
  --wait \                  # Block until response is ready
  --timeout 180 \           # Seconds to wait (default: 300)
  --mcp exa                 # Attach MCP servers (can repeat)
```

### Supported Tools

| Tool | Flag | Notes |
|------|------|-------|
| Claude | `--tool claude` | Default, no flag needed |
| Codex | `--tool codex` | Requires `codex` CLI installed |
| Gemini | `--tool gemini` | Requires `gemini` CLI installed |

### How It Works

1. Script auto-detects current session and profile
2. Creates a child session with the specified tool in the parent's project directory
3. Waits for the tool to initialize (handles Codex approval prompts automatically)
4. Sends the question/prompt
5. With `--wait`: polls until the agent responds, then returns the full output
6. Without `--wait`: returns immediately, check output later with `agent-deck session output "Title"`

### Examples

```bash
# Code review from Codex
$SKILL_DIR/scripts/launch-subagent.sh "Codex Review" "Read cmd/main.go and suggest improvements" --tool codex --wait --timeout 180

# Architecture feedback from Gemini
$SKILL_DIR/scripts/launch-subagent.sh "Gemini Arch" "Review the project structure and suggest better patterns" --tool gemini --wait --timeout 180

# Both in parallel (consult both, compare answers)
$SKILL_DIR/scripts/launch-subagent.sh "Ask Codex" "Best way to handle errors in Go?" --tool codex --wait --timeout 120 &
$SKILL_DIR/scripts/launch-subagent.sh "Ask Gemini" "Best way to handle errors in Go?" --tool gemini --wait --timeout 120 &
wait
```

### Cleanup

After getting the response, remove the consultation session:

```bash
agent-deck remove "Consult Codex"
# Or remove multiple at once:
agent-deck remove "Codex Review" && agent-deck remove "Gemini Arch"
```

## Peer (Root) Sessions vs Sub-Agents

**The default — sub-agent linkage:** `agent-deck launch` and `agent-deck add`, when invoked from *inside* an existing agent-deck session, automatically link the new session as a child of the calling session (sets `parent_session_id`, inherits the parent's group when `-g` is omitted, and grants `--add-dir` to the parent's project path). This is usually what you want for short-lived work sessions (plan / verify / release / consult).

**When the default is wrong — root-level peer sessions:** if you are creating a session that should stand independently at the root — a peer conductor, a standalone project session, a session that should outlive the current one, or anything that semantically is NOT a child of the calling session — pass the `-no-parent` flag.

| Use case | Parent linkage | Flag |
|---|---|---|
| Plan / impl / verify worker for the current task | ✅ child | (default) |
| Consultation (codex / gemini / research) | ✅ child | (default) |
| Another conductor (root-level peer) | ❌ child | `-no-parent` |
| Project session unrelated to current work | ❌ child | `-no-parent` |
| Session intended to outlive the caller | ❌ child | `-no-parent` |

```bash
# Root-level peer conductor, no parent linkage:
agent-deck launch ~/projects/foo -t "conductor-foo" -g "conductor" -c claude -no-parent -m "..."

# Verify after spawn:
agent-deck list --json | jq '.[] | select(.title=="conductor-foo") | .parent_session_id'
# Must print: null
```

**Symptoms you created a sub-agent when you wanted a peer:**
- `parent_session_id` is non-null in `list --json` output
- The new session's baked `pane_start_command` contains `--add-dir <caller's path>` even though you gave it a different project path
- Transition events for the new session's children flow to the caller instead of the new peer
- Event routing and heartbeat parent-linkage puts it under the caller's tree in the TUI

**Fix for an already-created sub-agent:** `agent-deck session unset-parent <session>` (alias: `session update <session> --no-parent`) removes the link in place and clears the stored parent project path, so the `--add-dir` grant is gone after the next `session restart`. To start completely clean instead, stop + remove the session and re-launch with `-no-parent`.

**Note on the launch-subagent.sh script:** that script is specifically designed to create sub-agents (the name says so). It does NOT support `-no-parent`. For peer sessions, skip the script and invoke `agent-deck launch -no-parent` directly.
