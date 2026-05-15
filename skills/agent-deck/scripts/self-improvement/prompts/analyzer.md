# Analyzer prompt — one conductor transcript

You are analyzing ONE distilled conductor conversation transcript to surface agent-deck usage signal. You are a short-lived worker session. Read the input, write the output, exit.

## Your task

1. Read the distilled transcript at:
   **`{INPUT_PATH}`**
2. Write a report at:
   **`{OUTPUT_PATH}`**
   using the EXACT schema below.
3. After writing, print `DONE: {OUTPUT_PATH}` to stdout and exit. Do not propose further work.

## Input format

The input is a chronological markdown timeline. Each line is one event:

```
[uuid-prefix] YYYY-MM-DDTHH:MM:SS TYPE: content
```

Event types you'll see:
- `PROMPT:`     a user prompt (their actual ask)
- `USER:`       a user message in the transcript
- `ASSIST:`     the assistant's text response (first 500 chars)
- `TOOL:`       a tool call (`tool-name | abbreviated args`)
- `ERROR:`      a tool error (these are the most actionable signal)
- `HEARTBEAT:`  a periodic system check (low signal — just count, don't dwell)
- `SKILL_LOAD:` a skill was loaded (note which one)

## Hard rules

- Read the input file fully before writing anything.
- Be SPECIFIC. "Often" is not specific; "3 of 5 times" is.
- CITE event uuid-prefixes (e.g. `[abc12345]`) when making claims so we can trace back.
- Do NOT speculate beyond what the transcript shows.
- Do NOT file GitHub issues. Do NOT run agent-deck commands. Do NOT touch any files except the output path.
- If the transcript is signal-poor (very short, all heartbeats), say so honestly in the "Honest assessment" section. Don't pad.
- Capabilities are GENERAL nouns ("Manage sessions", "Attach MCPs", "Manage conductors"), not flags or specific commands.

## Privacy (CRITICAL — this report may be shared publicly)

Write the report as if it were already public on GitHub. Do NOT include:

- Real people's names — use `<user>`, `<colleague>`, `<person-A>`
- Email addresses, phone numbers, physical locations
- Real company / customer / project names — use `<client>`, `<company>`, `<project>` (agent-deck itself is fine)
- Absolute paths with usernames — write `~/...` or `<path>`
- IP addresses, hostnames, internal domains
- API tokens, passwords, secret values, env-var values (even if visible in the transcript)
- Specific worktree / branch names that hint at private work — write `<branch>`
- tmux session titles that include user content — write `<session>`

DO include:
- agent-deck command names and tool names (`agent-deck launch`, `Bash`, `Edit`, etc.)
- Public file names (README.md, SKILL.md, CHANGELOG.md, go.mod, etc.)
- Bug shapes, reproducers, severity levels
- Citations to event uuid-prefixes from the input (these get sanitized later)

When in doubt, write a generic placeholder. The bug is the signal — names and paths are not.

## Output schema (write EXACTLY this structure)

```markdown
# Report: agent-deck / {SID}

## Summary
- Date range: <first event ts> → <last event ts>
- Total events kept (rough): <count>
- Tool calls: <count>
- Errors: <count>
- Top 3 tools by frequency: <name (N), name (N), name (N)>

## agent-deck capabilities exercised
List each general agent-deck capability you saw in use, with frequency and outcome.
Format:
- **<Capability name>** — N times — succeeded / failed / mixed — <one-line note>

(Examples of capability names: Manage sessions, Manage conductors, Manage watchers, Attach MCPs, Attach skills, Worktree workflows, Heartbeat orchestration, GitHub pipeline oversight, Sub-agent spawning, Channel routing.)

## Issues encountered
Bulleted. For each:
- **What broke:** <one line>
- **Citation:** [uuid-prefix]
- **Type:** tool-error | user-reported | inferred-friction
- **Reproducible from this transcript?:** yes / unclear / no

## User corrections / interventions
Where the user pushed back or redirected the assistant.
- **User said (paraphrased):** "..."
- **Was attempting:** ...
- **Citation:** [uuid-prefix]

## Workflow patterns observed
Generic, reusable patterns visible here. Keep generic (no session-specific details).
- **Pattern:** ...
- **Citation:** [uuid-prefix]

## Capability discovery
If the conductor did something not obviously documented as a capability — clever combinations, undocumented flags, novel workflows — note it. Otherwise: "None observed."

## Honest assessment
2–3 sentences. Was this transcript signal-rich or signal-poor? What was specifically useful or wasted? Worth a human re-read?

END.
```
