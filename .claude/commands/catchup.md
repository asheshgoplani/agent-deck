# Context Restoration

Rebuild context after `/clear` or starting a new session.

## Instructions

### 1. Check Git Status

```bash
# Current branch and status
git status

# Recent commits
git log --oneline -10

# Changes since last commit
git diff --stat

# Staged changes
git diff --cached --stat
```

### 2. Identify Current Work

- What branch are we on?
- What feature/task is in progress?
- Are there uncommitted changes?
- What was the last commit about?

### 3. Read Key Context Files

```bash
# Project guidelines
cat CLAUDE.md

# Session handoff (primary context restore)
cat .claude/plans/SESSION-HANDOFF.md 2>/dev/null || echo "No session handoff file"

# Recent decisions
cat .claude/plans/DECISIONS.md 2>/dev/null || echo "No decisions file"

# Current todos
cat .claude/plans/TO-DOS.md 2>/dev/null || echo "No todos file"
```

### 4. Summarize Recent Changes

Review recently modified files:

```bash
# Files changed in last 5 commits
git diff --name-only HEAD~5..HEAD

# Most recently modified files
ls -lt src/**/*.{ts,tsx} 2>/dev/null | head -10
```

### 5. Check for In-Progress Work

- Open TODOs in code (`grep -r "TODO" src/`)
- Failing tests (`npm run test:run`)
- Build errors (`npm run build`)

## Output Format

```
## Session Context Restored

### Current State
- Branch: `feature/xyz`
- Last commit: "feat: add email threading"
- Uncommitted changes: 3 files

### Session Handoff
[Summary from .claude/plans/SESSION-HANDOFF.md]

### In Progress
[Description of current work based on changes]

### Recent History
1. [Commit 1 summary]
2. [Commit 2 summary]
3. [Commit 3 summary]

### Open Tasks
- [ ] [Task from .claude/plans/TO-DOS.md]

### Key Decisions
- [Recent decisions from .claude/plans/DECISIONS.md]

### Next Steps
Based on the context, you should probably:
1. [Suggested next action]
2. [Suggested next action]

### Key Files to Review
- `src/components/X.tsx` - Recently modified
- `src/hooks/useY.ts` - In progress
```

## Use After

- Running `/clear`
- Starting a new Claude Code session
- Returning after a break
- Switching between branches
