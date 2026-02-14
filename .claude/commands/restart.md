# Session Restart & Context Preservation

Prepare for a fresh session by saving all current context, progress, and decisions.

**Use this command when context is running low and you need to run `/clear` or restart Claude Code.**

## Instructions

### 1. Save Current Progress

#### Update .claude/plans/TO-DOS.md

Create or update `.claude/plans/TO-DOS.md` with current task status:

```markdown
# Current Tasks

## In Progress

- [ ] [Current task being worked on]

## Completed This Session

- [x] [Task 1]
- [x] [Task 2]

## Pending

- [ ] [Next task]
- [ ] [Future task]

## Blocked

- [ ] [Blocked task] - Reason: [why blocked]
```

#### Update .claude/plans/DECISIONS.md

Create or update `.claude/plans/DECISIONS.md` with any decisions made:

```markdown
# Architecture Decisions

## [Date] - [Decision Title]

**Context**: [Why this decision was needed]
**Decision**: [What was decided]
**Consequences**: [Impact of this decision]
```

### 2. Update Memory (MCP)

If using the memory MCP server, store key context:

```
Key entities to remember:
- Current feature being built
- Architectural decisions made
- Blockers encountered
- Next steps planned
```

### 3. Commit Current Work

**IMPORTANT**: Before clearing, commit all progress to git.

Run the `/git:commit` command or manually:

```bash
# Stage all changes
git add -A

# Create a work-in-progress commit
git commit -m "wip: [current feature] - saving progress before session restart

Current state:
- [What's working]
- [What's in progress]
- [What's next]"

# Push to remote
git push
```

### 4. Create Session Handoff

Write a brief summary to `.claude/plans/SESSION-HANDOFF.md`:

```markdown
# Session Handoff - [Date/Time]

## What Was Accomplished

- [List of completed work]

## Current State

- Branch: `[branch-name]`
- Feature: [feature being built]
- Status: [percentage complete or description]

## Open Issues

- [Issue 1]
- [Issue 2]

## Next Steps (in order)

1. [Immediate next step]
2. [Following step]
3. [After that]

## Important Context

- [Key decision or insight]
- [Gotcha to remember]
- [File that needs attention]

## Commands to Run First

\`\`\`bash
npm install # if deps changed
npm run dev # start dev server
\`\`\`
```

### 5. Final Checklist

- [ ] .claude/plans/TO-DOS.md updated
- [ ] .claude/plans/DECISIONS.md updated (if decisions made)
- [ ] All changes committed
- [ ] Changes pushed to remote
- [ ] .claude/plans/SESSION-HANDOFF.md created
- [ ] Memory updated (if using MCP memory)

## Output

```
## Session Restart Prepared

### Files Updated
- .claude/plans/TO-DOS.md - [X] tasks tracked
- .claude/plans/DECISIONS.md - [X] decisions recorded
- .claude/plans/SESSION-HANDOFF.md - Created

### Git Status
- All changes committed: "[commit message]"
- Pushed to origin/[branch]

### Memory
- Key context stored in MCP memory

### Ready to Clear
You can now safely run `/clear` or restart Claude Code.

To resume, run `/catchup` in the new session.
```

## Quick Version

If short on time, at minimum:

```bash
git add -A && git commit -m "wip: saving progress" && git push
```

Then create a quick `.claude/plans/SESSION-HANDOFF.md` with next steps.
