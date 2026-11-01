package session

import (
	"fmt"
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
	writeTokenFile(freshStartDir, f.child.ID, ".json", freshStartToken{At: at, SessionID: f.child.ClaudeSessionID})
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

func (f *turnTestFixture) status(childStatus string) map[string]string {
	return map[string]string{f.child.ID: childStatus, f.parent.ID: "waiting"}
}

// hookHistory writes child id's hook event history, the events stamped now in
// order, as the hook handler does.
func hookHistory(t *testing.T, id string, events ...string) {
	t.Helper()
	var b strings.Builder
	for n, ev := range events {
		fmt.Fprintf(&b, "{\"at\":%q,\"event\":%q}\n", time.Now().Add(time.Duration(n)*time.Millisecond).UTC().Format(time.RFC3339Nano), ev)
	}
	path := filepath.Join(filepath.Dir(hookStatusFilePath(id)), id+".events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

const otherConversation = "99999999-8888-4777-8666-555555555555"

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
		// a hook event since the start lands: until then, not yet. Past the
		// boot timeout that wait, and the wait for the pane, ends as on main.
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
		"hooks never fire: past the boot timeout as on main": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			f.freshStart(time.Now().Add(-3 * time.Minute))
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
		// Spent, so a later prompt adds no line after the urgent.
		"pane not drawn within the boot timeout: news, token spent": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.substate = string(SubstateNone)
			f.freshStart(time.Now().Add(-3 * time.Minute))
			hookHistory(t, f.child.ID, "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The boot timeout does not end the token: a child launched with no
		// kickoff and acknowledged later (waiting -> idle, a new poll key, no
		// transcript) is still at the boot, pending or read.
		"acknowledged past the boot timeout, no turn": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStart(time.Now().Add(-3 * time.Minute))
			hookHistory(t, f.child.ID, "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.status("waiting"), nil)
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{freshStartLine}, true},
		"acknowledged past the boot timeout after the parent read the line": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStart(time.Now().Add(-3 * time.Minute))
			hookHistory(t, f.child.ID, "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.status("waiting"), nil)
			if got := f.drain(t); len(got) != 1 {
				t.Fatalf("want the line drained, got %+v", got)
			}
			f.d.sweepFreshStartTokens(f.byID, time.Now())
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, nil, true},
		// A SessionEnd since the start may be an older process's, landing
		// after the token was written: no proof this one's turns are seen.
		"a SessionEnd since the start is not the proof": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now()}
			f.observeAllPaths(f.idle())
		}, nil, true},
		// The hook file keeps only the last event: a turn then a /clear leaves
		// a SessionStart and a new, empty transcript. A row saved on another
		// conversation than the start's spends the token.
		"saved on another conversation than the start's": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.child.ClaudeSessionID = otherConversation
			f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), true)
		}, []string{"urgent: "}, false},
		// A turn falls between passes and its Stop is overwritten in the hook
		// file before any pass reads it; the saved id does not move and no
		// transcript resolves. The history and the hook file's id still show it.
		"a turn, then a SessionStart of another conversation (#3010 review)": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "UserPromptSubmit", "Stop", "SessionStart")
			st.hook = sessionStartNow()
			st.hook.SessionID = otherConversation
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"a turn in the history only": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "Stop")
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"a second SessionStart in the history": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "SessionStart")
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		"a SessionStart of another conversation in the hook file only": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook.SessionID = otherConversation
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The old process's async SessionEnd lands over the new SessionStart in
		// the hook file; the history still holds that SessionStart.
		"a late SessionEnd over the new SessionStart": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			hookHistory(t, f.child.ID, "SessionStart", "SessionEnd")
			st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now()}
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine}, true},
		// With the history off or missing, turns may go unseen: the token ends
		// at the boot timeout, as on main.
		"no hook event history, past the boot timeout": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.freshStart(time.Now().Add(-3 * time.Minute))
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		}, []string{"urgent: "}, false},
		// The transcript path resolving shows turns would be seen, with no
		// hook event yet.
		"an empty transcript, no hook event": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			st.hook = nil
			if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine}, true},
		"two fresh starts in a row: a line each": {func(t *testing.T, f *turnTestFixture, st *pidStub) {
			f.observeAllPaths(f.idle())
			f.freshStart(time.Now())
			st.hook = sessionStartNow()
			f.d.recordTerminalTurns("default", f.byID, f.status("running"), nil)
			f.bootSeen()
			f.observeAllPaths(f.idle())
		}, []string{freshStartLine, freshStartLine}, true},
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
		{"past the boot timeout, no turn", time.Minute, func(t *testing.T, f *turnTestFixture) { hookHistory(t, f.child.ID, "SessionStart") }, true},
		{"past the boot timeout, no hook event history", time.Minute, nil, false},
		{"past the boot timeout, only an old process's late SessionEnd", time.Minute, func(t *testing.T, f *turnTestFixture) { hookHistory(t, f.child.ID, "SessionEnd") }, false},
		{"a day, no turn", 25 * time.Hour, nil, false},
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
		// The builder leaves CLAUDE_CONFIG_DIR to a path command: the child
		// runs under the ambient dir, where the hooks are.
		{"configured path to a claude binary, hooks only in the ambient dir", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\ncommand = \"/opt/tools/bin/claude\"\nconfig_dir = \""+filepath.Join(home, "other-claude")+"\"\n")
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
		{"hooks not installed in the child's config dir", func(t *testing.T, home string, inst *Instance) string {
			writeConfigTOML(t, home, "[claude]\nconfig_dir = \""+filepath.Join(home, "other-claude")+"\"\n")
			return inst.buildClaudeCommand("claude")
		}, true, false},
		{"a config that does not load", func(t *testing.T, home string, inst *Instance) string {
			cmd := inst.buildClaudeCommand("claude")
			writeConfigTOML(t, home, "[claude\n")
			return cmd
		}, true, false},
		{"startup query: a turn starts at once", func(t *testing.T, home string, inst *Instance) string {
			inst.StartupQuery = "reply pong"
			return inst.buildClaudeCommand("claude")
		}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			installClaudeHooks(t, home)
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
			if ok != c.token || (ok && (!tok.At.Equal(at) || tok.SessionID != inst.ClaudeSessionID)) {
				t.Fatalf("token %v %+v, want a token=%v at the spawn time for %s (command %s)", ok, tok, c.token, inst.ClaudeSessionID, cmd)
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
			home := isolatedHomeDir(t)
			installClaudeHooks(t, home)
			inst := newGuardInstance(t, home)
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

// A restart leaves no fresh-start token, and the resume builder's note is not
// carried to a later spawn. A resumed boot is the restart token's to judge. A
// never-interacted child's restart (--session-id) gets none either: its old
// process's exit would spend one first, and holding that exit back takes the
// restart token, which a child with no transcript does not get.
func TestIssue2954_RestartLeavesNoToken(t *testing.T) {
	t.Run("resuming a conversation", func(t *testing.T) { restartLeavesNoToken(t, "--resume") })
	t.Run("never interacted", func(t *testing.T) { restartLeavesNoToken(t, "--session-id") })
	// A failed restart started nothing: the start's token stays. A restart
	// that lost the spawn lock to a concurrent Start spawned nothing either:
	// that Start keeps its token, written after the restart read the token
	// (won) or before (startFirst, Start already holding the lock).
	for name, c := range map[string]struct {
		account         string
		won, startFirst bool
	}{"a failed restart": {account: "no-such-slot"}, "lost the spawn lock to a concurrent start": {won: true},
		"lost the spawn lock to a start that wrote its token first": {startFirst: true}} {
		t.Run(name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			inst := newGuardInstance(t, home)
			inst.Account = c.account
			old := time.Now().Add(-time.Minute)
			writeTokenFile(freshStartDir, inst.ID, ".json", freshStartToken{At: old})
			newer := time.Now()
			prevLock := instanceSpawnLockAcquireFn
			t.Cleanup(func() { instanceSpawnLockAcquireFn = prevLock })
			instanceSpawnLockAcquireFn = func(id string) (func(), error) {
				if c.won { // the concurrent Start, done while this restart waited
					writeTokenFile(freshStartDir, id, ".json", freshStartToken{At: newer})
				}
				if c.won || c.startFirst {
					recordInstanceSpawn(id)
				}
				return prevLock(id)
			}
			if err := inst.Restart(); (err != nil) != (c.account != "") {
				t.Fatalf("precondition: restart error %v", err)
			}
			want := old
			if c.won {
				want = newer
			}
			if tok, ok := readFreshStartToken(inst.ID); !ok || !tok.At.Equal(want) {
				t.Fatalf("token %+v (%v), want the one at %v", tok, ok, want)
			}
		})
	}
}

func restartLeavesNoToken(t *testing.T, flag string) {
	skipIfNoTmuxBinary(t)
	home := isolatedHomeDir(t)
	installClaudeHooks(t, home)
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
	if flag == "--resume" {
		stageConversation(t, home, inst.ProjectPath, inst.ClaudeSessionID)
	}
	if cmd := inst.buildClaudeResumeCommand(); !strings.Contains(cmd, flag) {
		t.Fatalf("precondition: the restart runs %s, got %s", flag, cmd)
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

// installClaudeHooks installs agent-deck's Claude hooks in home's ~/.claude.
func installClaudeHooks(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if _, err := InjectClaudeHooks(dir); err != nil || !CheckClaudeHooksInstalled(dir) {
		t.Fatalf("install hooks in %s: %v", dir, err)
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
