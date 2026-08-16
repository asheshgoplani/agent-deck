package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// writeCodexRollout drops a minimal user-thread rollout JSONL for sessionID
// under codexHome, matching the layout codexRolloutPathInHome globs for
// (codexHome/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl).
func writeCodexRollout(t *testing.T, codexHome, sessionID string) string {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "08", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll rollout dir: %v", err)
	}
	path := filepath.Join(dir, "rollout-2026-08-15T10-00-00-"+sessionID+".jsonl")
	head := `{"type":"session_meta","payload":{"thread_source":"cli","source":"cli"}}` + "\n"
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return path
}

// #1929: the #756 resume-existence gate resolved the codex home with the
// instance-blind getCodexHomeDirForCommand while the launch exported the
// account-aware codexHomeToExport(). For a session whose account maps to
// [profiles.<account>.codex].config_dir the rollout lives under the ACCOUNT
// home but was looked for in the DEFAULT one, so the gate always missed and
// silently dropped the binding — every restart started a fresh conversation.
func TestIssue1929_AccountRolloutSurvivesTheResumeGate(t *testing.T) {
	workHome := codexAccountHome(t)
	sid := "a1111111-2222-4333-8444-555555555551"
	writeCodexRollout(t, workHome, sid)

	detectedAt := time.Now().Add(-time.Minute)
	inst := &Instance{
		ID: "i1929a", Title: "t", Tool: "codex", Command: "codex",
		Account:         "work",
		CodexSessionID:  sid,
		CodexDetectedAt: detectedAt,
	}

	cmd := inst.buildCodexCommand("codex")

	if inst.CodexSessionID != sid {
		t.Fatalf("resume gate cleared the session id: CodexSessionID = %q, want %q — the rollout exists under the account home", inst.CodexSessionID, sid)
	}
	if inst.CodexDetectedAt.IsZero() {
		t.Errorf("resume gate cleared CodexDetectedAt for a session with a live rollout")
	}
	if !strings.Contains(cmd, "resume "+sid) {
		t.Errorf("launch command does not resume the bound session.\ngot: %s", cmd)
	}
}

// The subagent-fork safety net reads the same home. With the instance-blind
// lookup it could never see an account session's rollout, so a poisoned
// subagent binding launched as `resume` and died on the first typed message.
func TestIssue1929_SubagentGateSeesTheAccountHome(t *testing.T) {
	workHome := codexAccountHome(t)
	sid := "a1111111-2222-4333-8444-555555555552"
	path := writeCodexRollout(t, workHome, sid)
	head := `{"type":"session_meta","payload":{"thread_source":"subagent","parent_thread_id":"p"}}` + "\n"
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatalf("rewrite rollout head: %v", err)
	}

	inst := &Instance{
		ID: "i1929b", Title: "t", Tool: "codex", Command: "codex",
		Account:        "work",
		CodexSessionID: sid,
	}

	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "fork "+sid) {
		t.Errorf("subagent binding was not forked — the gate looked in the wrong codex home.\ngot: %s", cmd)
	}
}

// A session with no account keeps resolving through the env/profile/global
// chain, so the gate must still find rollouts in the default home.
func TestIssue1929_NoAccountResumeGateUnchanged(t *testing.T) {
	tmp := withTempHome(t)
	t.Setenv("CODEX_HOME", "")
	globalHome := filepath.Join(tmp, "codex-global")
	if err := os.MkdirAll(filepath.Join(tmp, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &UserConfig{}
	cfg.Codex.ConfigDir = globalHome
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	sid := "a1111111-2222-4333-8444-555555555553"
	writeCodexRollout(t, globalHome, sid)

	inst := &Instance{
		ID: "i1929c", Title: "t", Tool: "codex", Command: "codex",
		CodexSessionID: sid,
	}
	cmd := inst.buildCodexCommand("codex")
	if inst.CodexSessionID != sid {
		t.Fatalf("resume gate cleared a valid binding in the global codex home")
	}
	if !strings.Contains(cmd, "resume "+sid) {
		t.Errorf("launch command does not resume the bound session.\ngot: %s", cmd)
	}
}

// A genuinely stale binding (no rollout anywhere) must still be dropped — the
// #756 gate's whole purpose. Guards against "fix" by disabling the gate.
func TestIssue1929_StaleBindingStillDropped(t *testing.T) {
	codexAccountHome(t)
	sid := "a1111111-2222-4333-8444-555555555554"

	inst := &Instance{
		ID: "i1929d", Title: "t", Tool: "codex", Command: "codex",
		Account:         "work",
		CodexSessionID:  sid,
		CodexDetectedAt: time.Now(),
	}
	cmd := inst.buildCodexCommand("codex")
	if inst.CodexSessionID != "" {
		t.Errorf("stale binding survived the resume gate: %q", inst.CodexSessionID)
	}
	if strings.Contains(cmd, "resume ") {
		t.Errorf("launch command resumes an unresumable session.\ngot: %s", cmd)
	}
}

// #1929: CreateForkedCodexInstanceWithOptions never copied Account onto the
// fork target, so the fork lost the selected account: its launch exported the
// default codex home and could not find the parent rollout it was forking.
func TestIssue1929_ForkCarriesTheAccount(t *testing.T) {
	workHome := codexAccountHome(t)
	sid := "a1111111-2222-4333-8444-555555555555"
	writeCodexRollout(t, workHome, sid)

	parent := &Instance{
		ID: "i1929e", Title: "parent", Tool: "codex", Command: "codex",
		ProjectPath:    t.TempDir(),
		Account:        "work",
		CodexSessionID: sid,
	}

	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if forked.Account != "work" {
		t.Errorf("fork lost the account: Account = %q, want %q", forked.Account, "work")
	}
	want := "CODEX_HOME=" + shellescape.Quote(workHome) + " "
	if !strings.Contains(cmd, want) {
		t.Errorf("fork command does not export the account's codex home.\nwant substring: %s\ngot: %s", want, cmd)
	}
}
