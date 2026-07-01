package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestWebMutator_FailedMultiFieldPATCH_RollsBack: a multi-field PATCH where a
// later field is rejected must leave the live Instance UNCHANGED (atomicity).
// Here env applies first (orderedUpdateFields), then plugins — claude-only —
// fails on a gemini session, so the whole PATCH must roll back including env.
func TestWebMutator_FailedMultiFieldPATCH_RollsBack(t *testing.T) {
	h, _ := newHeadlessHomeForTest(t, "_test_rollback")
	inst := &session.Instance{
		ID:          "rb-1",
		Title:       "t",
		ProjectPath: "/tmp/rb-proj",
		GroupPath:   session.DefaultGroupPath,
		Command:     "gemini",
		Tool:        "gemini",
		Status:      session.StatusStopped,
		CreatedAt:   time.Now(),
		Env:         []string{"OLD=1"},
	}
	h.instances = append(h.instances, inst)
	h.instanceByID[inst.ID] = inst

	m := NewWebMutator(h)
	_, _, err := m.UpdateSession("rb-1", map[string]string{
		session.FieldEnv:     "NEW=2",
		session.FieldPlugins: "octo",
	})
	if err == nil {
		t.Fatal("expected the PATCH to fail (plugins unsupported on a gemini session)")
	}
	if got := strings.Join(inst.Env, ","); got != "OLD=1" {
		t.Fatalf("env must roll back to OLD=1 after a failed multi-field PATCH; got %q", got)
	}
}

// TestWebMutator_FailedPATCH_RollsBackToolOptions covers a cross-field SIDE
// EFFECT: SetField(FieldTool) clears ToolOptionsJSON when leaving claude. PATCH
// {tool:gemini, plugins:octo} applies tool first (clears claude options), then
// plugins rejects (not claude) — the full-snapshot rollback must restore BOTH
// Tool and ToolOptionsJSON, not just the field's own value.
func TestWebMutator_FailedPATCH_RollsBackToolOptions(t *testing.T) {
	h, _ := newHeadlessHomeForTest(t, "_test_rollback_opts")
	inst := &session.Instance{
		ID:          "rb-2",
		Title:       "t",
		ProjectPath: "/tmp/rb2-proj",
		GroupPath:   session.DefaultGroupPath,
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusStopped,
		CreatedAt:   time.Now(),
	}
	// Populate ToolOptionsJSON via a claude-only field.
	if _, _, err := session.SetField(inst, session.FieldSkipPermissions, "true", nil); err != nil {
		t.Fatalf("seed skip-permissions: %v", err)
	}
	wantOpts := string(inst.ToolOptionsJSON)
	if wantOpts == "" {
		t.Fatal("precondition: ToolOptionsJSON should be populated")
	}

	h.instances = append(h.instances, inst)
	h.instanceByID[inst.ID] = inst
	m := NewWebMutator(h)

	_, _, err := m.UpdateSession("rb-2", map[string]string{
		session.FieldTool:    "gemini",
		session.FieldPlugins: "octo",
	})
	if err == nil {
		t.Fatal("expected the PATCH to fail (plugins unsupported once tool is gemini)")
	}
	if inst.Tool != "claude" {
		t.Fatalf("tool must roll back to claude, got %q", inst.Tool)
	}
	if string(inst.ToolOptionsJSON) != wantOpts {
		t.Fatalf("ToolOptionsJSON must roll back; got %q want %q", inst.ToolOptionsJSON, wantOpts)
	}
}

// TestWebMutator_PATCH_RejectsEnvOnSSHRemotePath: the web PATCH FieldEnv path
// bypasses session.SetField, so it must re-apply the SSH remote-path guard —
// otherwise env would persist on a session where it is silently dropped at
// spawn. A non-empty env set is rejected; an empty-list clear is allowed. The
// session is seeded into storage because the headless mutator hydrates from it.
func TestWebMutator_PATCH_RejectsEnvOnSSHRemotePath(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_ssh_env")
	inst := &session.Instance{
		ID:            "ssh-1",
		Title:         "t",
		ProjectPath:   "/tmp/ssh-proj",
		GroupPath:     session.DefaultGroupPath,
		Command:       "claude",
		Tool:          "claude",
		Status:        session.StatusStopped,
		CreatedAt:     time.Now(),
		SSHHost:       "host",
		SSHRemotePath: "/remote/path",
		Env:           []string{"OLD=1"},
	}
	all := []*session.Instance{inst}
	if err := storage.SaveWithGroups(all, session.NewGroupTree(all)); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}
	m := NewWebMutator(h)

	// Non-empty set is rejected and leaves the persisted env untouched.
	if _, _, err := m.UpdateSession("ssh-1", map[string]string{session.FieldEnv: "NEW=2"}); err == nil {
		t.Fatal("expected env set on an SSH remote-path session to be rejected")
	}
	if got := strings.Join(h.instanceByID["ssh-1"].Env, ","); got != "OLD=1" {
		t.Fatalf("env must be unchanged after a rejected set; got %q", got)
	}

	// Clearing (empty list) is still allowed so stale env can be removed.
	if _, _, err := m.UpdateSession("ssh-1", map[string]string{session.FieldEnv: ""}); err != nil {
		t.Fatalf("clearing env on an SSH remote-path session must be allowed; got %v", err)
	}
	if got := h.instanceByID["ssh-1"].Env; len(got) != 0 {
		t.Fatalf("env must be cleared; got %v", got)
	}
}
