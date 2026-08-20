// Issue #924 follow-up — `session fork --account <name>`.
//
// The defect these lock down (found by the verify-all session, 2026-08-19,
// rows 1h/1i): the override was written onto the fork's RECORD after the
// one-shot fork command had already been baked against the parent's home. The
// record claimed one account, the process ran under another, and the two only
// converged on a later restart that drops --resume and strands the
// conversation.
//
// The invariant asserted here is the fix: the account on the record and the
// account the launched command exports are the same value, because the command
// is built FROM the record. When that cannot be arranged — unknown account, no
// transcript to carry, a tool whose history cannot be migrated — nothing is
// created at all, so no record can misreport what it runs under.
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const forkAccountTOML = `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"
`

// forkAccountFixture builds a parent claude session on account "work" whose
// conversation exists on disk, and returns it with the temp HOME and the
// conversation id.
func forkAccountFixture(t *testing.T) (tmpHome string, parent *Instance, sid string) {
	t.Helper()
	tmpHome = withTempAgentDeckHome(t, forkAccountTOML)

	proj := filepath.Join(tmpHome, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	sid = "11111111-2222-3333-4444-555555555555"
	workProjDir := filepath.Join(tmpHome, ".claude-work", "projects", ConvertToClaudeDirName(proj))
	if err := os.MkdirAll(workProjDir, 0o700); err != nil {
		t.Fatalf("mkdir work project dir: %v", err)
	}
	body := strings.Repeat(`{"type":"user","message":"parent transcript"}`+"\n", 40)
	if err := os.WriteFile(filepath.Join(workProjDir, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	parent = &Instance{
		ID:               "fork-acct-parent",
		Tool:             "claude",
		Title:            "parent",
		ProjectPath:      proj,
		Account:          "work",
		ClaudeSessionID:  sid,
		ClaudeDetectedAt: time.Now(),
	}
	parent.markClaudeSessionIDVerified()
	if !parent.CanFork() {
		t.Fatalf("fixture parent is not forkable")
	}
	return tmpHome, parent, sid
}

// accountHomeInCommand extracts the CLAUDE_CONFIG_DIR the baked command
// exports. Returns "" when the command exports none.
func accountHomeInCommand(t *testing.T, cmd string) string {
	t.Helper()
	const key = "export CLAUDE_CONFIG_DIR="
	idx := strings.Index(cmd, key)
	if idx < 0 {
		return ""
	}
	rest := cmd[idx+len(key):]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("malformed CLAUDE_CONFIG_DIR export in %q", cmd)
	}
	value := strings.TrimSpace(rest[:end])
	value = strings.Trim(value, `"'`)
	// #1858: the export may be expressed relative to $HOME so it resolves on
	// the machine the session runs on. Resolve it the way the shell would.
	if strings.HasPrefix(value, "$HOME") {
		value = os.Getenv("HOME") + strings.TrimPrefix(value, "$HOME")
	}
	return value
}

// TestForkAccountOverride_ProcessRunsUnderTheAccountTheRecordClaims is the
// F2 regression: the fork must launch with the OVERRIDE's home, with its
// conversation present there, and keep --resume so the context survives.
func TestForkAccountOverride_ProcessRunsUnderTheAccountTheRecordClaims(t *testing.T) {
	tmpHome, parent, sid := forkAccountFixture(t)

	forked, _, err := parent.CreateForkedInstanceForTool("child", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Account != "work" {
		t.Fatalf("fork should inherit the parent's account first, got %q", forked.Account)
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	moved, err := ApplyForkAccountOverride(parent, forked, cfg, "personal", nil)
	if err != nil {
		t.Fatalf("ApplyForkAccountOverride: %v", err)
	}
	if moved == nil {
		t.Fatal("expected a result for a non-empty override")
	}

	personalHome := filepath.Join(tmpHome, ".claude-personal")

	// 1. The record says "personal".
	if forked.Account != "personal" {
		t.Errorf("record account = %q, want %q", forked.Account, "personal")
	}
	// 2. The command the process will run exports personal's home. This is the
	//    half that used to disagree with the record.
	if got := accountHomeInCommand(t, forked.Command); got != personalHome {
		t.Errorf("baked CLAUDE_CONFIG_DIR = %q, want %q\ncommand: %s", got, personalHome, forked.Command)
	}
	// 3. The conversation is actually there, or --resume finds nothing.
	dstFile := filepath.Join(personalHome, "projects", ConvertToClaudeDirName(parent.ProjectPath), sid+".jsonl")
	info, statErr := os.Stat(dstFile)
	if statErr != nil {
		t.Fatalf("conversation not carried into the target account: %v", statErr)
	}
	if moved.MigratedPath != dstFile {
		t.Errorf("MigratedPath = %q, want %q", moved.MigratedPath, dstFile)
	}
	// 4. Copy-only: the source account keeps its copy, byte for byte.
	srcFile := filepath.Join(tmpHome, ".claude-work", "projects", ConvertToClaudeDirName(parent.ProjectPath), sid+".jsonl")
	srcInfo, srcErr := os.Stat(srcFile)
	if srcErr != nil {
		t.Fatalf("source conversation was removed: %v", srcErr)
	}
	if srcInfo.Size() != info.Size() {
		t.Errorf("copied conversation is %d bytes, source is %d", info.Size(), srcInfo.Size())
	}
	// 5. The fork still resumes the parent's conversation — the failure mode of
	//    the rejected "rebake the command first" fix was a fork that resumed
	//    nothing at all.
	if !strings.Contains(forked.Command, "--resume "+sid) || !strings.Contains(forked.Command, "--fork-session") {
		t.Errorf("fork command lost its resume: %s", forked.Command)
	}
	// 6. The parent's own id is untouched, so the baked --resume still names it.
	if parent.ClaudeSessionID != sid {
		t.Errorf("parent conversation id was rewritten to %q, want %q", parent.ClaudeSessionID, sid)
	}
	// 7. Start() must run the rebuilt command verbatim (#745).
	if !forked.IsForkAwaitingStart {
		t.Error("IsForkAwaitingStart cleared: Start() would rebuild the command and drop --resume")
	}
	// 8. The fork's own minted id matches the one baked as --session-id.
	if forked.ClaudeSessionID == "" || !strings.Contains(forked.Command, `--session-id "`+forked.ClaudeSessionID+`"`) {
		t.Errorf("record claude_session_id %q does not match the baked --session-id\ncommand: %s",
			forked.ClaudeSessionID, forked.Command)
	}
}

// TestFork_RecordAndLaunchedAccountAlwaysAgree is the create-time recording
// invariant, over both fork shapes: whatever account ends up on the record,
// the command the fork launches with exports that account's home.
func TestFork_RecordAndLaunchedAccountAlwaysAgree(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override string
		want     string
	}{
		{name: "inherited", override: "", want: "work"},
		{name: "overridden", override: "personal", want: "personal"},
		{name: "override names the inherited account", override: "work", want: "work"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpHome, parent, sid := forkAccountFixture(t)
			forked, _, err := parent.CreateForkedInstanceForTool("child", "", nil)
			if err != nil {
				t.Fatalf("CreateForkedInstanceForTool: %v", err)
			}
			cfg, err := LoadUserConfig()
			if err != nil {
				t.Fatalf("LoadUserConfig: %v", err)
			}
			if _, err := ApplyForkAccountOverride(parent, forked, cfg, tc.override, nil); err != nil {
				t.Fatalf("ApplyForkAccountOverride(%q): %v", tc.override, err)
			}
			if forked.Account != tc.want {
				t.Fatalf("record account = %q, want %q", forked.Account, tc.want)
			}
			wantHome := filepath.Join(tmpHome, ".claude-"+tc.want)
			if got := accountHomeInCommand(t, forked.Command); got != wantHome {
				t.Errorf("record says %q but the command launches under %q (want %q)\ncommand: %s",
					forked.Account, got, wantHome, forked.Command)
			}
			if !strings.Contains(forked.Command, "--resume "+sid) {
				t.Errorf("fork command lost its resume: %s", forked.Command)
			}
		})
	}
}

// TestForkAccountOverride_UnknownAccountRefuses: an account with no home for
// this tool used to be a warning that still wrote the name onto the record,
// leaving a session reporting an account it did not run under.
func TestForkAccountOverride_UnknownAccountRefuses(t *testing.T) {
	_, parent, _ := forkAccountFixture(t)
	forked, originalCmd, err := parent.CreateForkedInstanceForTool("child", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	cfg, _ := LoadUserConfig()

	moved, err := ApplyForkAccountOverride(parent, forked, cfg, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected a refusal for an account with no configured home")
	}
	if moved != nil {
		t.Error("no result should be returned on refusal")
	}
	if forked.Account != "work" {
		t.Errorf("record was mutated to %q despite the refusal", forked.Account)
	}
	if forked.Command != originalCmd {
		t.Error("fork command was mutated despite the refusal")
	}
	if !strings.Contains(err.Error(), "nonexistent") || !strings.Contains(err.Error(), "personal, work") {
		t.Errorf("error should name the account and the configured ones, got: %v", err)
	}
}

// TestForkAccountOverride_NoConversationRefuses: with no transcript on disk
// there is nothing to carry across, so the fork would resume nothing under the
// target account. Refuse rather than create it.
func TestForkAccountOverride_NoConversationRefuses(t *testing.T) {
	tmpHome, parent, sid := forkAccountFixture(t)
	forked, _, err := parent.CreateForkedInstanceForTool("child", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	// Remove the transcript AFTER the fork command was baked, mimicking a
	// record whose conversation is not where it claims to be.
	srcFile := filepath.Join(tmpHome, ".claude-work", "projects", ConvertToClaudeDirName(parent.ProjectPath), sid+".jsonl")
	if err := os.Remove(srcFile); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	cfg, _ := LoadUserConfig()
	if _, err := ApplyForkAccountOverride(parent, forked, cfg, "personal", nil); err == nil {
		t.Fatal("expected a refusal when no conversation can be located")
	}
	if forked.Account != "work" {
		t.Errorf("record was mutated to %q despite the refusal", forked.Account)
	}
	if _, statErr := os.Stat(filepath.Join(tmpHome, ".claude-personal", "projects")); statErr == nil {
		t.Error("nothing should have been written into the target account's home")
	}
}

// TestForkAccountOverride_NonClaudeToolRefused: the migration is claude's
// transcript layout. A codex fork must be told so, not silently recorded onto
// an account whose rollout it does not have.
func TestForkAccountOverride_NonClaudeToolRefused(t *testing.T) {
	tmpHome := withTempAgentDeckHome(t, `
[profiles.work.codex]
codex_home = "~/.codex-work"

[profiles.personal.codex]
codex_home = "~/.codex-personal"
`)
	parent := &Instance{
		ID:          "codex-parent",
		Tool:        "codex",
		Title:       "parent",
		ProjectPath: filepath.Join(tmpHome, "proj"),
		Account:     "work",
	}
	forked := &Instance{
		ID:          "codex-fork",
		Tool:        "codex",
		Title:       "child",
		ProjectPath: parent.ProjectPath,
		Account:     "work",
	}
	cfg, _ := LoadUserConfig()
	_, err := ApplyForkAccountOverride(parent, forked, cfg, "personal", nil)
	if err == nil {
		t.Fatal("expected a refusal for a codex fork")
	}
	if !strings.Contains(err.Error(), "claude sessions only") {
		t.Errorf("error should say why, got: %v", err)
	}
	if forked.Account != "work" {
		t.Errorf("record was mutated to %q despite the refusal", forked.Account)
	}
}

// TestForkAccountOverride_EmptyIsNoOp: no --account means no behaviour change
// at all — the inherited command must not be rebuilt.
func TestForkAccountOverride_EmptyIsNoOp(t *testing.T) {
	_, parent, _ := forkAccountFixture(t)
	forked, originalCmd, err := parent.CreateForkedInstanceForTool("child", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	cfg, _ := LoadUserConfig()
	moved, err := ApplyForkAccountOverride(parent, forked, cfg, "   ", nil)
	if err != nil {
		t.Fatalf("empty override must be a no-op, got: %v", err)
	}
	if moved != nil {
		t.Error("empty override should return no result")
	}
	if forked.Command != originalCmd || forked.Account != "work" {
		t.Error("empty override must not touch the fork")
	}
}
