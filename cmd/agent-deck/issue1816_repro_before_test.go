package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// REPRODUCTION for #1816, the BEFORE half.
//
// This file deliberately uses only symbols that existed BEFORE the fix, so it
// compiles and passes on the pre-fix tip. It is the executable statement of the
// defect: four sessions on ONE credential produce FOUR independent escalations
// and nothing anywhere names the credential they share.
//
// No real credentials are involved. The auth hold is armed through its durable
// on-disk contract (the sidecar #1743 writes), which is the same thing the live
// detector produces when a pane renders a 401 banner — and no token material
// exists anywhere in this test.

// issue1816Account is the account slot all the sessions in the scenario share.
const issue1816Account = "work"

// setupIssue1816Fleet stands up n sessions attributed to one account whose
// config_dir is configured in a sandboxed HOME, and arms a real auth hold on
// each. Returns the instances and the credential directory they share.
func setupIssue1816Fleet(t *testing.T, n int) ([]*session.Instance, string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_DATA_HOME")
	os.Unsetenv("XDG_STATE_HOME")
	os.Unsetenv("XDG_CACHE_HOME")
	// CLAUDE_CONFIG_DIR sits above profile/global in the chain; clear it so the
	// scenario resolves through the account slot it is actually about.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")

	credDir := filepath.Join(home, ".claude-work")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cred dir: %v", err)
	}

	adDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	cfg := fmt.Sprintf("[profiles.%s.claude]\nconfig_dir = %q\n", issue1816Account, credDir)
	if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	instances := make([]*session.Instance, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("worker-%d", i+1)
		inst := &session.Instance{
			ID:      fmt.Sprintf("id-%s", title),
			Title:   title,
			Tool:    "claude",
			Status:  session.StatusError,
			Account: issue1816Account,
		}
		armIssue1816AuthHold(t, inst)
		instances = append(instances, inst)
	}

	// The scenario is only meaningful if all of them really do resolve to one
	// credential directory.
	for _, inst := range instances {
		if got := session.GetClaudeConfigDirForInstance(inst); got != credDir {
			t.Fatalf("%s resolved to %q, want the shared credential dir %q", inst.Title, got, credDir)
		}
	}
	return instances, credDir
}

// armIssue1816AuthHold writes the durable auth-hold sidecar #1743 defines, which
// is what puts a session in the auth-401 hold every surface reads.
func armIssue1816AuthHold(t *testing.T, inst *session.Instance) {
	t.Helper()

	dataDir, err := agentpaths.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	// Create the "runtime" marker so EffectiveDataPath resolves to this dir
	// rather than falling through to the legacy location.
	holdDir := filepath.Join(dataDir, "runtime", "auth-hold")
	if err := os.MkdirAll(holdDir, 0o700); err != nil {
		t.Fatalf("mkdir auth-hold: %v", err)
	}

	rec := session.AuthHoldRecord{
		InstanceID: inst.ID,
		Tool:       "claude",
		Reason:     session.AuthHoldReasonDeath,
		// Evidence is the rendered banner. It is a failure message, not a
		// credential: no token, key, or secret appears here or anywhere else.
		Evidence:  "API Error: 401 " + `{"type":"error","error":{"type":"authentication_error"}}`,
		Timestamp: 1786000000,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal hold: %v", err)
	}
	if err := os.WriteFile(filepath.Join(holdDir, inst.ID+".json"), data, 0o600); err != nil {
		t.Fatalf("write hold: %v", err)
	}

	if got := inst.AuthHold(); got == nil {
		t.Fatalf("auth hold did not arm for %s (sidecar at %s)", inst.Title, holdDir)
	}
	if held, _ := inst.IsAuthHeld(); !held {
		t.Fatalf("%s is not auth-held after arming", inst.Title)
	}
}

// perSessionEscalations is the view a conductor gets today: one escalation per
// held session, exactly as `session show --json <id>` reports it.
func perSessionEscalations(instances []*session.Instance) []string {
	var out []string
	for _, inst := range instances {
		held, remedy := inst.IsAuthHeld()
		if !held {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s", inst.Title, remedy))
	}
	return out
}

// THE DEFECT: one dead credential, four independent escalations, and not one of
// them says which credential is dead.
func TestIssue1816_Before_OneDeadCredentialProducesNSeparateEscalations(t *testing.T) {
	instances, credDir := setupIssue1816Fleet(t, 4)

	escalations := perSessionEscalations(instances)

	t.Logf("BEFORE — %d sessions on ONE credential (%s) produce %d separate escalations:",
		len(instances), credDir, len(escalations))
	for i, e := range escalations {
		t.Logf("  [%d/%d] %s", i+1, len(escalations), e)
	}

	if len(escalations) != len(instances) {
		t.Fatalf("got %d escalations for %d held sessions, want one each", len(escalations), len(instances))
	}
	// This is the whole complaint: nothing in the per-session view identifies
	// the shared credential, so a conductor cannot collapse these itself.
	//
	// The remedy is checked on its own rather than the logged "title: remedy"
	// line, because the session titles here legitimately contain the account
	// name as a prefix ("worker-1") and matching that would prove nothing.
	for _, inst := range instances {
		_, remedy := inst.IsAuthHeld()
		if strings.Contains(remedy, credDir) || strings.Contains(remedy, issue1816Account) {
			t.Errorf("per-session remedy unexpectedly names the credential — the defect would already be fixed: %s", remedy)
		}
	}
}
