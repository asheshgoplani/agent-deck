package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2954: about a second after `agent-deck launch` a Claude child sits at
// its empty prompt, before its kickoff is typed, and the daemon delivered that
// edge as urgent. The start leaves a token; the child's first arrival at its
// empty prompt with no turn since the start is one info line, and everything
// else is delivered as before.

const freshStartLine = "info: Started; at its prompt, no turn yet."

func (f *turnTestFixture) freshStart(at time.Time) {
	writeTokenFile(freshStartDir, f.child.ID, ".json", freshStartToken{At: at})
}

func hasFreshStartToken(id string) bool {
	_, ok := readFreshStartToken(id)
	return ok
}

func (f *turnTestFixture) freshTokenLeft() bool { return hasFreshStartToken(f.child.ID) }

// pending is the parent's pending records, as "<urgent|info>: <text>".
func (f *turnTestFixture) pending(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range f.inboxRecords(t) {
		tier := "info"
		if r.IsUrgent() {
			tier = "urgent"
		}
		out = append(out, tier+": "+r.Text)
	}
	return out
}

func (f *turnTestFixture) idle() map[string]string {
	return map[string]string{f.child.ID: "idle", f.parent.ID: "waiting"}
}

// sessionStartNow is the fresh process's SessionStart in its hook file.
func sessionStartNow() *HookStatus {
	return &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: time.Now()}
}

// The child after a fresh start, with its pane at the empty prompt and its
// SessionStart in the hook file unless the case says otherwise, and no
// transcript yet (Claude writes it with the first prompt): what each case's
// sightings leave in the parent's inbox, and whether the token is still there.
func TestIssue2954_FreshStartBoot(t *testing.T) {
	type freshCase struct {
		run  func(t *testing.T, f *turnTestFixture, st *pidStub)
		want []string
		left bool
	}
	cases := map[string]freshCase{
		"seen on every path in one pass": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine}, true},
		"read by the parent, seen again by a restarted daemon": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			if got := f.drain(t); len(got) != 1 {
				t.Fatalf("want the line drained, got %+v", got)
			}
			f.restartDaemon()
			f.observeAllPaths(f.idle())
		}, nil, true},
		// Turns are not provably visible until the transcript path resolves or
		// a hook event since the start lands: until then, not yet.
		"at the prompt before SessionStart lands, then it lands": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			f.observeAllPaths(f.idle())
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"the previous process's Stop on file, then SessionStart": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			at := time.Now().Add(-2 * time.Second)
			st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: at, ReceivedAt: at}
			f.observeAllPaths(f.idle())
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"hooks never fire: past the token's life as on main": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			f.freshStart(time.Now().Add(-3 * time.Minute))
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"a flip stamped before the token": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now().Add(-time.Minute), true)
		}, []string{"urgent: "}, true},
		"a top-level conductor: nothing sent": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.child.Title, f.child.ParentSessionID = "conductor-self", f.child.ID
			f.observeAllPaths(f.idle())
			for _, inbox := range []string{UnownedInboxID, f.child.ID} {
				if events, _ := ReadInboxEvents(inbox); len(events) != 0 {
					t.Fatalf("no record in %s, got %+v", inbox, events)
				}
			}
		}, nil, true},
		"pane not drawn yet, then the prompt": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(SubstateNone)
			f.observeAllPaths(f.idle())
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"pane not drawn within the token's life": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(SubstateNone)
			f.freshStart(time.Now().Add(-3 * time.Minute))
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"no parent: nothing sent, nothing unowned": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.child.ParentSessionID = ""
			f.observeAllPaths(f.idle())
			if events, _ := ReadInboxEvents(UnownedInboxID); len(events) != 0 {
				t.Fatalf("no unowned record, got %+v", events)
			}
		}, nil, true},
		"a commit that fails for now is retried by the poll pass": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			inboxes := filepath.Dir(InboxPathFor(f.parent.ID))
			if err := os.RemoveAll(inboxes); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inboxes, nil, 0o600); err != nil { // a file where the inbox dir goes
				t.Fatal(err)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if err := os.Remove(inboxes); err != nil {
				t.Fatal(err)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"the kickoff's turn after the line": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			f.appendTurn(t, fxHuman("u1", "reply pong"), fxAssistantText("a1", "pong"))
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}, []string{freshStartLine, "info: pong"}, false},
		"a prompt submitted (hook file)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = &HookStatus{Status: "running", Event: "UserPromptSubmit", UpdatedAt: time.Now().Add(time.Second)}
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}, []string{"urgent: "}, false},
		"a turn ended, received after the start (hook file)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now().Add(-time.Minute), ReceivedAt: time.Now()}
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
		}, []string{"urgent: "}, false},
		"an error": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true)
		}, []string{"urgent: "}, false},
	}
	// A pane in a recognised state other than its empty prompt is delivered
	// as before and spends nothing: the next arrival at the prompt is the boot.
	for _, sub := range []Substate{SubstateInteractiveMenu, SubstateAuth401, SubstateModelUnavailable} {
		cases["pane shows "+string(sub)+", then the prompt"] = freshCase{func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(sub)
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), true)
		}, []string{"urgent: ", freshStartLine}, true}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.freshStart(time.Now().Add(-time.Second))
			st.hook = sessionStartNow()
			c.run(t, f, st)
			if got := f.pending(t); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("records:\n got %q\nwant %q", got, c.want)
			}
			if !strings.Contains(strings.Join(c.want, ""), "urgent") && (*f.sends != 0 || InboxHasUrgentPending(f.parent.ID)) {
				t.Fatalf("the boot must not wake the parent or block its Stop, sends=%d", *f.sends)
			}
			if f.freshTokenLeft() != c.left {
				t.Fatalf("token left = %v, want %v", !c.left, c.left)
			}
		})
	}
}

// With the TUI owning the statuses the daemon has no cached substate and reads
// the pane, once, for a token holder only; a cached substate is used as is.
func TestIssue2954_PaneReadOnlyForATokenHolder(t *testing.T) {
	for _, c := range []struct {
		name   string
		token  bool
		cached Substate
		reads  int
		want   string
	}{
		{"TUI: nothing cached", true, SubstateNone, 1, freshStartLine},
		{"cached at its prompt", true, SubstateIdleAtEmptyPrompt, 0, freshStartLine},
		{"cached menu (as on main)", true, SubstateInteractiveMenu, 0, "urgent: "},
		{"no token (as on main)", false, SubstateNone, 0, "urgent: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			stubRestartPIDs(t).hook = sessionStartNow()
			reads := 0
			restartPaneSubstate = func(*Instance) string { reads++; return string(SubstateIdleAtEmptyPrompt) }
			prevCached := turnCachedSubstate
			t.Cleanup(func() { turnCachedSubstate = prevCached })
			turnCachedSubstate = func(*Instance) Substate { return c.cached }
			if c.token {
				f.freshStart(time.Now())
			}
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), true)
			if got := f.pending(t); len(got) != 1 || got[0] != c.want || reads != c.reads {
				t.Fatalf("records %q, pane reads %d; want %q, %d", got, reads, c.want, c.reads)
			}
		})
	}
}

// The sweep only spends tokens, and sends nothing.
func TestIssue2954_SweepSpendsSilently(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "30s")
	for _, c := range []struct {
		name  string
		age   time.Duration
		setup func(t *testing.T, f *turnTestFixture)
		left  bool
	}{
		{"within the boot timeout", 10 * time.Second, nil, true},
		{"past the boot timeout", time.Minute, nil, false},
		{"a turn", 10 * time.Second, func(t *testing.T, f *turnTestFixture) {
			f.appendTurn(t, fxHuman("u1", "reply pong"), fxAssistantText("a1", "pong"))
		}, false},
		{"unreadable", 10 * time.Second, func(t *testing.T, f *turnTestFixture) {
			path, _ := runtimeTokenPath(freshStartDir, f.child.ID, ".json")
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"gone from the profile, young", time.Hour, func(t *testing.T, f *turnTestFixture) { delete(f.byID, f.child.ID) }, true},
		{"gone from the profile a day", 25 * time.Hour, func(t *testing.T, f *turnTestFixture) { delete(f.byID, f.child.ID) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			stubRestartPIDs(t)
			f.freshStart(time.Now().Add(-c.age))
			if c.setup != nil {
				c.setup(t, f)
			}
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			if got := f.pending(t); len(got) != 0 {
				t.Fatalf("the sweep sends nothing, got %q", got)
			}
			if f.freshTokenLeft() != c.left {
				t.Fatalf("token left = %v, want %v", !c.left, c.left)
			}
		})
	}
}

// A token is spent by its time: a start that wrote a newer token since the
// daemon read the old one keeps its own.
func TestIssue2954_SpendKeepsANewerToken(t *testing.T) {
	f := newTurnTestFixture(t)
	newer := time.Now()
	f.freshStart(newer)
	spendFreshStartToken(f.child.ID, freshStartToken{At: newer.Add(-time.Second)})
	if !f.freshTokenLeft() {
		t.Fatalf("spending an older token must keep the newer one")
	}
}

// Each command the builders make, and whether its spawn leaves a token: one
// that starts a new conversation and whose turns this host can see. A start
// that leaves none removes the one an earlier start left.
func TestIssue2954_FreshCommandsLeaveAToken(t *testing.T) {
	const ownID = "abcdef01-2345-4678-8abc-def012345678"
	resumeMode := func(t *testing.T, inst *Instance) {
		inst.ClaudeSessionID = ownID
		opts := NewClaudeOptions(nil)
		opts.SessionMode, opts.ResumeSessionID = "resume", ownID
		if err := inst.SetClaudeOptions(opts); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		build func(t *testing.T, home string, inst *Instance) string
		fresh bool // the command starts a new conversation
		token bool
	}{
		{"new session: minted --session-id", func(t *testing.T, home string, inst *Instance) string {
			return inst.buildClaudeCommand("claude")
		}, true, true},
		{"continue mode, no conversation in the directory", func(t *testing.T, home string, inst *Instance) string {
			opts := NewClaudeOptions(nil)
			opts.SessionMode = "continue"
			if err := inst.SetClaudeOptions(opts); err != nil {
				t.Fatal(err)
			}
			return inst.buildClaudeCommand("claude")
		}, true, true},
		{"resume mode, never interacted", func(t *testing.T, home string, inst *Instance) string {
			resumeMode(t, inst)
			return inst.buildClaudeCommand("claude")
		}, true, true},
		{"resume mode, a conversation", func(t *testing.T, home string, inst *Instance) string {
			stageConversation(t, home, inst.ProjectPath, ownID)
			resumeMode(t, inst)
			return inst.buildClaudeCommand("claude")
		}, false, false},
		{"start of a bound id, never interacted", func(t *testing.T, home string, inst *Instance) string {
			inst.ClaudeSessionID = ownID
			return inst.buildClaudeResumeCommand()
		}, true, true},
		{"start of a bound id, a conversation", func(t *testing.T, home string, inst *Instance) string {
			stageConversation(t, home, inst.ProjectPath, ownID)
			inst.ClaudeSessionID = ownID
			return inst.buildClaudeResumeCommand()
		}, false, false},
		{"bound id refused by the identity guard", func(t *testing.T, home string, inst *Instance) string {
			const foreign = "cccccccc-3333-4444-8555-666666666666"
			stageConversation(t, home, inst.ProjectPath, foreign)
			inst.adoptDiscoveredClaudeSessionID(foreign)
			return inst.buildClaudeResumeCommand()
		}, true, true},
		{"configured path to a claude binary", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\ncommand = \"/opt/tools/bin/claude\"\n")
			return inst.buildClaudeCommand("claude")
		}, true, true},
		// Fresh, but its turns may not show on this host: no token.
		{"configured alias that picks its own config dir", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\ncommand = \"cdw\"\n")
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"inline env before the binary", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\ncommand = \"CLAUDE_CONFIG_DIR=/elsewhere claude\"\n")
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"SSH", func(t *testing.T, home string, inst *Instance) string {
			inst.SSHHost = "build-box"
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"sandboxed", func(t *testing.T, home string, inst *Instance) string {
			inst.Sandbox = &SandboxConfig{Enabled: true}
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"behind its own wrapper", func(t *testing.T, home string, inst *Instance) string {
			inst.Wrapper = "ssh build-box {command}"
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"behind its tool definition's wrapper", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[tools.myclaude]\ncommand = \"claude\"\ncompatible_with = \"claude\"\nwrapper = \"env CLAUDE_CONFIG_DIR=/elsewhere {command}\"\n")
			inst.Tool = "myclaude"
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"Claude hooks turned off", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\nhooks_enabled = false\n")
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"startup query: a turn starts at once", func(t *testing.T, home string, inst *Instance) string {
			inst.StartupQuery = "reply pong"
			return inst.buildClaudeCommand("claude")
		}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			inst := newGuardInstance(t, home)
			inst.ID = "fresh-2954"
			cmd := c.build(t, home, inst)
			if c.fresh != (strings.Contains(cmd, "--session-id") && !strings.Contains(cmd, "--resume")) {
				t.Fatalf("precondition: fresh=%v but the command is %s", c.fresh, cmd)
			}
			writeTokenFile(freshStartDir, inst.ID, ".json", freshStartToken{At: time.Now().Add(-time.Hour)})
			at := time.Now()
			noteFreshStart(inst, at)
			tok, ok := readFreshStartToken(inst.ID)
			if ok != c.token || (ok && !tok.At.Equal(at)) {
				t.Fatalf("token %v at %v, want a token=%v at the spawn time (command %s)", ok, tok.At, c.token, cmd)
			}
			// The note is used once: a later spawn that built nothing (a fork
			// target's prebuilt command) is not fresh.
			noteFreshStart(inst, time.Now())
			if hasFreshStartToken(inst.ID) {
				t.Fatalf("the builder's note is used once")
			}
		})
	}
}

// A fresh start whose spawn fails (its project directory is gone, which tmux
// refuses before anything runs) leaves no token, through either start path.
func TestIssue2954_FailedSpawnLeavesNoToken(t *testing.T) {
	for name, start := range map[string]func(*Instance) error{
		"Start":            (*Instance).Start,
		"StartWithMessage": func(i *Instance) error { return i.StartWithMessage("reply pong") },
	} {
		t.Run(name, func(t *testing.T) {
			inst := newGuardInstance(t, isolatedHomeDir(t))
			inst.ID = "fresh-2954-fail"
			if err := os.RemoveAll(inst.ProjectPath); err != nil {
				t.Fatal(err)
			}
			if err := start(inst); err == nil || !strings.Contains(err.Error(), "failed to start tmux session") {
				_ = inst.Kill()
				t.Fatalf("precondition: the spawn must fail, got %v", err)
			}
			if hasFreshStartToken(inst.ID) {
				t.Fatalf("a failed spawn leaves no token")
			}
		})
	}
}

// A resuming restart leaves no fresh-start token (its boot is the restart's to
// judge, or delivered as before), and the resume builder's note is not carried
// to a later spawn.
func TestIssue2954_RestartLeavesNoToken(t *testing.T) {
	skipIfNoTmuxBinary(t)
	home := isolatedHomeDir(t)
	bin := filepath.Join(t.TempDir(), "claude") // a plain claude binary, as far as the builders can tell
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigTOML(t, home, "[claude]\ncommand = \""+bin+"\"\n")
	inst := newGuardInstance(t, home)
	inst.Title = "fresh-2954-restart"
	if err := inst.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	if !hasFreshStartToken(inst.ID) {
		t.Fatalf("precondition: a fresh start leaves a token")
	}
	if err := inst.Restart(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if hasFreshStartToken(inst.ID) {
		t.Fatalf("a restart leaves no token")
	}
	noteFreshStart(inst, time.Now())
	if hasFreshStartToken(inst.ID) {
		t.Fatalf("a restart leaves no note for a later spawn")
	}
}

// writeConfigTOML writes home's config.toml.
func writeConfigTOML(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
}
