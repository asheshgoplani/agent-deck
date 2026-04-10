# Visual Verification Prompt for Claude Code

Use this as a prompt for a Claude Code session that will verify agent-deck screenshots.

## Usage

```bash
# Step 1: Generate screenshots
./scripts/visual-verify.sh /tmp/visual-verify

# Step 2: Have Claude Code verify them
claude --print "$(cat scripts/visual-verify-claude.md)" 
# Or interactively, paste the prompt below.
```

## Verification Prompt

You are verifying the agent-deck TUI (terminal UI) by examining screenshots.

Read each PNG file in `/tmp/visual-verify/` and compare against the checklist in `/tmp/visual-verify/CHECKLIST.md`.

For each screenshot:

1. Read the PNG using the Read tool
2. Describe what you see (layout, colors, text content, UI elements)
3. Go through each checklist item and mark PASS or FAIL
4. Note any visual issues: garbled text, misaligned elements, broken Unicode, color problems, clipped text

After reviewing all screenshots, provide a summary:

```
## Visual Verification Results

| Screenshot | Pass | Fail | Issues |
|------------|------|------|--------|
| 01_main_empty | X/Y | ... | ... |
| ... | ... | ... | ... |

### Overall: PASS / FAIL

### Issues Found:
- (list any failures with details)
```

Be strict. If something looks slightly off (misaligned by even one character, wrong color, clipped text), flag it. The goal is to catch rendering regressions before users see them.
