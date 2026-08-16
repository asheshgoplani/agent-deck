package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexAccountHome sets up an isolated HOME holding a config.toml that maps two
// account slots to their own codex homes, plus a global [codex] config_dir to
// prove the account outranks it.
func codexAccountHome(t *testing.T) (workHome string) {
	t.Helper()
	tmp := withTempHome(t)
	// withTempHome isolates HOME/XDG but not CODEX_HOME, which outranks the
	// config on the no-account fallback path — a developer with CODEX_HOME set
	// would see this suite fail on their own environment.
	t.Setenv("CODEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(tmp, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	workHome = filepath.Join(tmp, "codex-work")
	cfg := &UserConfig{
		Profiles: map[string]ProfileSettings{
			"work": {Codex: ProfileCodexSettings{ConfigDir: workHome}},
		},
	}
	cfg.Codex.ConfigDir = filepath.Join(tmp, "codex-global")
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	return workHome
}

// #1922: setting an account on a codex session recorded and reported the
// account but never applied [profiles.<account>.codex].config_dir, so the
// process launched against the default codex home. The mismatch surfaced only
// as a quota error on an account the user never chose.
func TestIssue1922_CodexAccountHomeIsExported(t *testing.T) {
	workHome := codexAccountHome(t)

	inst := &Instance{ID: "i1", Title: "t", Tool: "codex", Command: "codex", Account: "work"}

	if got := inst.codexHomeToExport(); got != workHome {
		t.Fatalf("codexHomeToExport() = %q, want the account's home %q", got, workHome)
	}
	if got := inst.getCodexHomeDir(); got != workHome {
		t.Errorf("getCodexHomeDir() = %q, want %q — resolution and launch must agree", got, workHome)
	}

	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "CODEX_HOME="+workHome+" ") {
		t.Errorf("launch command does not export the account's CODEX_HOME.\ngot: %s", cmd)
	}
}

// An account naming no codex block must fall through silently rather than
// exporting an empty CODEX_HOME, matching the permissive style of the Claude
// account chain.
func TestIssue1922_UnknownAccountFallsThrough(t *testing.T) {
	codexAccountHome(t)

	inst := &Instance{ID: "i2", Title: "t", Tool: "codex", Command: "codex", Account: "no-such-account"}

	if got := inst.accountCodexHomeDir(); got != "" {
		t.Errorf("accountCodexHomeDir() = %q, want \"\" for an account with no codex block", got)
	}
	cmd := inst.buildCodexCommand("codex")
	if strings.Contains(cmd, "CODEX_HOME= ") {
		t.Errorf("empty CODEX_HOME exported for an unmapped account.\ngot: %s", cmd)
	}
}

// A CODEX_HOME the user embedded in this session's own command is the most
// explicit statement there is and must still win over the account mapping.
func TestIssue1922_CommandEmbeddedHomeBeatsAccount(t *testing.T) {
	codexAccountHome(t)

	explicit := "/tmp/explicit-codex-home"
	inst := &Instance{
		ID: "i3", Title: "t", Tool: "codex",
		Command: "CODEX_HOME=" + explicit + " codex",
		Account: "work",
	}

	if got := inst.getCodexHomeDir(); got != explicit {
		t.Errorf("getCodexHomeDir() = %q, want the command-embedded %q", got, explicit)
	}
}

// A session with no account keeps the pre-existing chain untouched.
func TestIssue1922_NoAccountUnchanged(t *testing.T) {
	codexAccountHome(t)

	inst := &Instance{ID: "i4", Title: "t", Tool: "codex", Command: "codex"}
	if got := inst.accountCodexHomeDir(); got != "" {
		t.Errorf("accountCodexHomeDir() = %q, want \"\" when no account is set", got)
	}
	if got := inst.codexHomeToExport(); !strings.Contains(got, "codex-global") {
		t.Errorf("codexHomeToExport() = %q, want the configured global codex home", got)
	}
}
