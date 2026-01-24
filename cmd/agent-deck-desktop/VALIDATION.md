# Phase 6 Validation Checklist

## Automated Gate Criteria

### G6.1: Cmd+F Search in Scrollback ✓
**Test:**
1. Attach to a session with history (run `seq 1 100` first if needed)
2. Scroll up to see older content
3. Press Cmd+F
4. Search for text that's in scrollback but not visible (e.g., "50")
5. Verify search finds and highlights the match
6. Press Enter to go to next match
7. Verify it jumps through all matches in scrollback

**Expected:**
- Search finds text in scrollback (not just visible area)
- Can navigate through all matches with Enter
- Highlights update correctly

**Notes:**
- Scrollback loads on attach (may see "Loading..." message briefly)
- This is critical for the value proposition vs plain tmux

---

### G6.2: Cmd+W Close with Confirmation ✓
**Test:**
1. Attach to a session
2. Press Cmd+W
3. Verify confirmation modal appears
4. Click "Cancel" - modal closes, stay in terminal
5. Press Cmd+W again
6. Click "Close Terminal" - return to session selector

**Expected:** Confirmation dialog prevents accidental closes

---

### G6.3: Powerlevel10k Renders
**Test:**
1. Start new terminal or attach to session
2. Verify powerlevel10k prompt displays:
   - Icons (Apple logo, folder icon, git branch)
   - Colors (directory path, git status)
   - Right-side prompt (time, status indicators)
   - Proper alignment

**Expected:** Prompt renders correctly without corruption

**Status:** ⬜ Needs manual verification

---

### G6.4: Autosuggestions Work
**Test:**
1. In terminal, type partial command: `git st`
2. Verify grayed-out autosuggestion appears: `git status`
3. Press right arrow to accept
4. Verify command completes

**Expected:** zsh-autosuggestions plugin works

**Status:** ⬜ Needs manual verification

---

### G6.5: Syntax Highlighting Works
**Test:**
1. Type command slowly: `ls -la /tmp`
2. Verify colors change as you type:
   - `ls` - command (green)
   - `-la` - flags (cyan/blue)
   - `/tmp` - path (underlined or different color)

**Expected:** zsh-syntax-highlighting plugin works

**Status:** ⬜ Needs manual verification

---

### G6.6: Performance with 10k Lines
**Test:**
1. Attach to session or new terminal
2. Run: `seq 1 10000`
3. Wait for output to complete
4. Scroll up and down rapidly
5. Verify smooth scrolling (no lag/jank)
6. Press Cmd+F, search for `5555`
7. Verify search works on large buffer

**Expected:**
- Output streams without blocking
- Scrolling is smooth (>30 fps feel)
- Search works on full buffer

**Status:** ⬜ Needs manual verification

---

### G6.7: All Tests Pass
**Test:**
```bash
# Backend
cd cmd/agent-deck-desktop
go build -o /dev/null .

# Frontend (if tests exist)
cd frontend
npm test
```

**Expected:** All tests pass

**Status:** ⬜ Needs verification

---

## Human Validation Gate

This is the **final checkpoint** before declaring the prototype complete. Answer these questions:

### Overall Experience
- [ ] Does the app feel **responsive**? (terminal input, navigation, search)
- [ ] Does it feel **better than the TUI** for your workflow?
- [ ] Would you **actually use this** for Agent Deck sessions?

### Visual Quality
- [ ] Does powerlevel10k render **as well as iTerm2**?
- [ ] Are colors **accurate** and **consistent**?
- [ ] Is font rendering **crisp** (no blurriness)?

### Functionality
- [ ] Can you **easily find** and **attach to sessions**?
- [ ] Does Cmd+F search feel **natural** and **useful**?
- [ ] Do keyboard shortcuts (Cmd+K, Cmd+W, Cmd+,) feel **intuitive**?

### Workflow Fit
- [ ] Does the session selector make **switching sessions easier**?
- [ ] Is the back button **discoverable** and **accessible**?
- [ ] Does the app **stay out of your way** when working?

### Deal Breakers
- [ ] Are there any **critical bugs** that make it unusable?
- [ ] Are there any **missing features** that block adoption?
- [ ] Is performance **good enough** for daily use?

---

## Known Limitations (Acceptable for Prototype)

These are **intentional trade-offs** for the prototype:

1. **Remote sessions disabled** - Only local tmux sessions work
2. **No scrollback pre-loading** - Attach shows current state, use tmux scrollback (Ctrl+B [)
3. **Content doesn't reflow on resize** - Standard terminal behavior
4. **Resize jitter** - Minor visual artifacts during window resize
5. **No session management** - Can't create/delete sessions from app
6. **No MCP management** - Can't attach/detach MCPs from app

These can be addressed in future iterations if the prototype validates the approach.

---

## Success Criteria

The prototype is **validated** if:

1. ✅ All automated gate criteria pass (G6.1-G6.7)
2. ✅ Human validation answers are mostly "yes"
3. ✅ User confirms: **"This is worth continuing development"**

## Next Steps After Validation

If prototype is validated:
1. Merge feature branch to main
2. Tag as `v0.1.0-prototype`
3. Document learnings and next iteration priorities
4. Plan v0.2.0 roadmap (remote sessions, session management, etc.)

If prototype needs iteration:
1. Document specific issues
2. Prioritize fixes
3. Re-run validation
