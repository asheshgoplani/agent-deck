package session

// A Claude session forked through extra args
// (extra_args = ["--resume","<parent>","--fork-session"]) re-forked its parent
// on every restart. The restart builder emits `--resume <own id>` and then
// appended the persisted extra args verbatim, so the process ran
//
//	claude --resume <own> ... --resume <parent> --fork-session
//
// and Claude (last --resume wins) forked the parent again instead of loading
// the fork's own conversation.
//
// Contract under test:
//   - the first start of such a fork still carries the fork pair;
//   - once the session has its own conversation id, every rebuilt command
//     carries exactly one --resume (its own id) and no --fork-session;
//   - a parent's stale selector never leaks into a native fork's command or
//     into the fork's persisted ExtraArgs;
//   - a stale pair already persisted in state.db is removed on load.

import (
	"strings"
	"testing"
	"time"
)

const (
	forkParentID = "55a741ca-0000-4000-8000-000000000001"
	forkOwnID    = "3774d945-0000-4000-8000-000000000002"
)

// forkArgTokens splits a built command into shell words (good enough for the
// bare UUIDs and flags asserted here).
func forkArgTokens(cmd string) []string { return strings.Fields(cmd) }

func forkArgCount(cmd, tok string) int {
	n := 0
	for _, f := range forkArgTokens(cmd) {
		if f == tok {
			n++
		}
	}
	return n
}

// forkArgResumeValue returns the value after the only --resume token.
func forkArgResumeValue(t *testing.T, cmd string) string {
	t.Helper()
	toks := forkArgTokens(cmd)
	for i, f := range toks {
		if f == "--resume" && i+1 < len(toks) {
			return strings.Trim(toks[i+1], `'"`)
		}
	}
	return ""
}

// assertOwnResumeOnly is the restart invariant for an extra-arg fork.
func assertOwnResumeOnly(t *testing.T, cmd, own string) {
	t.Helper()
	if n := forkArgCount(cmd, "--resume"); n != 1 {
		t.Fatalf("restart command must carry exactly one --resume, found %d:\n%s", n, cmd)
	}
	if got := forkArgResumeValue(t, cmd); got != own {
		t.Fatalf("restart must resume the session's own id %s, got %q:\n%s", own, got, cmd)
	}
	if strings.Contains(cmd, "--fork-session") {
		t.Fatalf("restart command must not carry --fork-session:\n%s", cmd)
	}
	if strings.Contains(cmd, forkParentID) {
		t.Fatalf("restart command must not name the parent conversation %s:\n%s", forkParentID, cmd)
	}
}

// newExtraArgFork models a Claude session created
// with --extra-arg --resume --extra-arg <parent> --extra-arg --fork-session.
func newExtraArgFork(t *testing.T) *Instance {
	t.Helper()
	inst := NewInstanceWithTool("extra-arg-fork", t.TempDir(), "claude")
	inst.ExtraArgs = []string{"--resume", forkParentID, "--fork-session", "--verbose"}
	return inst
}

// TestExtraArgForkRestartResumesOwnConversation is the reported bug:
// fork, the fork gets its own id, restart.
func TestExtraArgForkRestartResumesOwnConversation(t *testing.T) {
	extraArgsTestEnv(t)
	inst := newExtraArgFork(t)

	// First start: the fork pair is the whole point of the launch.
	first := inst.buildClaudeCommand("claude")
	if !strings.Contains(first, "--fork-session") || !strings.Contains(first, forkParentID) {
		t.Fatalf("first start of an extra-arg fork must keep --resume <parent> --fork-session:\n%s", first)
	}

	// The fork now has its own conversation, with data on disk.
	inst.ClaudeSessionID = forkOwnID
	inst.markClaudeSessionIDVerified()
	inst.ClaudeDetectedAt = time.Now()
	writeTranscript1349(t, inst, forkOwnID, 3)

	restart := inst.buildClaudeResumeCommand()
	assertOwnResumeOnly(t, restart, forkOwnID)
	if !strings.Contains(restart, "--verbose") {
		t.Fatalf("ordinary extra args must survive the strip:\n%s", restart)
	}
}

// TestExtraArgForkRestartWithoutTranscriptHasNoForeignResume covers the
// --session-id branch of the restart builder (own id, no jsonl yet).
func TestExtraArgForkRestartWithoutTranscriptHasNoForeignResume(t *testing.T) {
	extraArgsTestEnv(t)
	inst := newExtraArgFork(t)
	inst.ClaudeSessionID = forkOwnID
	inst.markClaudeSessionIDVerified()

	cmd := inst.buildClaudeResumeCommand()
	if n := forkArgCount(cmd, "--resume"); n != 0 {
		t.Fatalf("no-transcript restart must not carry any --resume, found %d:\n%s", n, cmd)
	}
	if strings.Contains(cmd, "--fork-session") || strings.Contains(cmd, forkParentID) {
		t.Fatalf("no-transcript restart must not re-fork the parent:\n%s", cmd)
	}
	if !strings.Contains(cmd, forkOwnID) {
		t.Fatalf("no-transcript restart must use the own id:\n%s", cmd)
	}
}

// TestExtraArgForkSelfHealOnRestartDropsStalePair: the restart path removes the
// stale pair from the instance itself, so the next save persists the repair.
func TestExtraArgForkSelfHealOnRestartDropsStalePair(t *testing.T) {
	extraArgsTestEnv(t)
	inst := newExtraArgFork(t)
	inst.ClaudeSessionID = forkOwnID
	inst.markClaudeSessionIDVerified()

	_ = inst.buildClaudeResumeCommand()
	if got := strings.Join(inst.ExtraArgs, " "); got != "--verbose" {
		t.Fatalf("restart must heal ExtraArgs to [--verbose], got %v", inst.ExtraArgs)
	}
}

// TestExtraArgForkSelfHealOnLoad: a stale pair persisted by an older build is removed
// when the session is loaded from state.db, while ordinary args stay.
func TestExtraArgForkSelfHealOnLoad(t *testing.T) {
	extraArgsTestEnv(t)
	s := newTestStorage(t)

	inst := newExtraArgFork(t)
	inst.ID = "extra-arg-fork-load"
	inst.ClaudeSessionID = forkOwnID
	inst.CreatedAt = time.Now()
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("want 1 instance, got %d", len(loaded))
	}
	if got := strings.Join(loaded[0].ExtraArgs, " "); got != "--verbose" {
		t.Fatalf("load must heal the stale fork pair, got ExtraArgs %v", loaded[0].ExtraArgs)
	}

	// A session that never started (no own id yet) keeps its fork args:
	// they are still needed for the first start.
	fresh := newExtraArgFork(t)
	fresh.ID = "extra-arg-fork-unstarted"
	fresh.CreatedAt = time.Now()
	if err := s.SaveWithGroups([]*Instance{fresh}, nil); err != nil {
		t.Fatalf("save unstarted: %v", err)
	}
	loaded, err = s.Load()
	if err != nil {
		t.Fatalf("load unstarted: %v", err)
	}
	for _, l := range loaded {
		if l.ID == "extra-arg-fork-unstarted" && !strings.Contains(strings.Join(l.ExtraArgs, " "), "--fork-session") {
			t.Fatalf("unstarted fork must keep its fork args, got %v", l.ExtraArgs)
		}
	}
}

// TestNativeForkOfStaleExtraArgForkParent: forking a session that still carries a
// stale pair must fork THAT session (not its parent) and must not persist the
// pair onto the new fork (#1407 copy).
func TestNativeForkOfStaleExtraArgForkParent(t *testing.T) {
	extraArgsTestEnv(t)
	parent := newForkableParent(t, []string{"--resume", forkParentID, "--fork-session", "--verbose"})

	forked, cmd, err := parent.CreateForkedInstanceWithOptions("extra-arg-fork-fork-of-fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceWithOptions: %v", err)
	}
	if n := forkArgCount(cmd, "--resume"); n != 1 {
		t.Fatalf("fork command must carry exactly one --resume, found %d:\n%s", n, cmd)
	}
	if got := forkArgResumeValue(t, cmd); got != parent.ClaudeSessionID {
		t.Fatalf("fork must resume the source session %s, got %q:\n%s", parent.ClaudeSessionID, got, cmd)
	}
	if n := forkArgCount(cmd, "--fork-session"); n != 1 {
		t.Fatalf("fork command must carry --fork-session exactly once, found %d:\n%s", n, cmd)
	}
	if strings.Contains(cmd, forkParentID) {
		t.Fatalf("fork command must not name the grandparent conversation:\n%s", cmd)
	}
	if got := strings.Join(forked.ExtraArgs, " "); got != "--verbose" {
		t.Fatalf("fork must persist only ordinary extra args, got %v", forked.ExtraArgs)
	}

	// And the fork's own restart is clean.
	forked.IsForkAwaitingStart = false
	forked.ClaudeSessionID = forkOwnID
	forked.markClaudeSessionIDVerified()
	forked.ClaudeDetectedAt = time.Now()
	writeTranscript1349(t, forked, forkOwnID, 3)
	assertOwnResumeOnly(t, forked.buildClaudeResumeCommand(), forkOwnID)
}

// TestStripStaleSessionSelectorArgs pins the token-level rules.
func TestStripStaleSessionSelectorArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		own     string
		kept    string
		dropped string
	}{
		{"no own id keeps everything", []string{"--resume", forkParentID, "--fork-session"}, "", "--resume " + forkParentID + " --fork-session", ""},
		{"stale pair", []string{"--resume", forkParentID, "--fork-session"}, forkOwnID, "", "--resume " + forkParentID + " --fork-session"},
		{"equals form and short flag", []string{"--model", "opus", "--resume=" + forkParentID, "--fork-session"}, forkOwnID, "--model opus", "--resume=" + forkParentID + " --fork-session"},
		{"short -r", []string{"-r", forkParentID, "--fork-session", "--agent", "x"}, forkOwnID, "--agent x", "-r " + forkParentID + " --fork-session"},
		{"session-id", []string{"--session-id", forkParentID, "--verbose"}, forkOwnID, "--verbose", "--session-id " + forkParentID},
		{"duplicate own resume", []string{"--resume", forkOwnID, "--verbose"}, forkOwnID, "--verbose", "--resume " + forkOwnID},
		{"continue", []string{"-c", "--verbose"}, forkOwnID, "--verbose", "-c"},
		{"bare resume before a flag", []string{"--resume", "--verbose"}, forkOwnID, "--verbose", "--resume"},
		{"bare fork-session with no foreign id is kept", []string{"--fork-session"}, forkOwnID, "--fork-session", ""},
		{"ordinary args untouched", []string{"--agent", "reviewer"}, forkOwnID, "--agent reviewer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept, dropped := stripStaleSessionSelectors(tc.args, tc.own)
			if got := strings.Join(kept, " "); got != tc.kept {
				t.Errorf("kept = %q, want %q", got, tc.kept)
			}
			if got := strings.Join(dropped, " "); got != tc.dropped {
				t.Errorf("dropped = %q, want %q", got, tc.dropped)
			}
		})
	}
}

// TestExtraArgForkRepairViaSessionSetExtraArgs pins the manual workaround for
// sessions on builds without the fix: `agent-deck session set <id>
// extra-args ""` (the CLI hands SetField the argv tokens [""]), save, load,
// restart. It must pass on builds without the fix as well as with it.
func TestExtraArgForkRepairViaSessionSetExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)
	s := newTestStorage(t)

	inst := newExtraArgFork(t)
	inst.ID = "extra-arg-fork-repair"
	inst.CreatedAt = time.Now()
	inst.ClaudeSessionID = forkOwnID

	if _, _, err := SetField(inst, FieldExtraArgs, "", []string{""}); err != nil {
		t.Fatalf("session set extra-args \"\": %v", err)
	}
	if inst.ExtraArgs != nil {
		t.Fatalf("session set extra-args \"\" must clear ExtraArgs, got %v", inst.ExtraArgs)
	}
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := s.Load()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v (%d rows)", err, len(loaded))
	}
	got := loaded[0]
	if len(got.ExtraArgs) != 0 {
		t.Fatalf("cleared extra args must stay cleared after save/load, got %v", got.ExtraArgs)
	}
	got.markClaudeSessionIDVerified()
	got.ClaudeDetectedAt = time.Now()
	writeTranscript1349(t, got, forkOwnID, 3)
	assertOwnResumeOnly(t, got.buildClaudeResumeCommand(), forkOwnID)
}
