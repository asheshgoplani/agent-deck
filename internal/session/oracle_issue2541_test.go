package session

// Black-box oracle for #2541 / PR #2544. Drives the real SetupConductor path
// (what `agent-deck conductor setup` calls) with a config.toml in a throwaway
// XDG home and inspects the resulting .claude/settings.json. Uses only APIs
// that exist on origin/main, so the same file runs on main and on the PR head.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func oracle2541Ask(t *testing.T, conductorDir string) (ask []string, hasAskKey bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(conductorDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var root struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings.json: %v\n%s", err, data)
	}
	raw, ok := root.Permissions["ask"]
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal(raw, &ask); err != nil {
		t.Fatalf("ask: %v", err)
	}
	return ask, true
}

func oracle2541Has(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func oracle2541Setup(t *testing.T, extraConductorCfg string, seed string) string {
	t.Helper()
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
	root := filepath.Join(t.TempDir(), "conductors")
	cfgDir := filepath.Join(xdgConfigHome, "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[conductor]\ndir = \"" + root + "\"\n" + extraConductorCfg
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	dir := filepath.Join(root, "alpha")
	if seed != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetupConductor("alpha", "default", false, false, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductor: %v", err)
	}
	return dir
}

const oracle2541Send = "Bash(agent-deck session send *)"
const oracle2541Launch = "Bash(agent-deck launch *)"

// A: permission_ask = false on a fresh conductor: no managed ask entries.
func TestOracle2541_A_OptOutFreshSetup(t *testing.T) {
	dir := oracle2541Setup(t, "permission_ask = false\n", "")
	ask, _ := oracle2541Ask(t, dir)
	if oracle2541Has(ask, oracle2541Send) || oracle2541Has(ask, oracle2541Launch) {
		t.Fatalf("permission_ask=false but setup wrote the managed ask list: %v", ask)
	}
}

// B: permission_ask = false over a settings.json that already has the managed
// list (an earlier setup) plus a user rule: managed entries go, user rule stays.
func TestOracle2541_B_OptOutRerunKeepsUserRule(t *testing.T) {
	seed := `{"permissions":{"ask":["Bash(git push *)","Bash(agent-deck session send *)","Bash(agent-deck launch *)"]},"theme":"dark"}`
	dir := oracle2541Setup(t, "permission_ask = false\n", seed)
	ask, _ := oracle2541Ask(t, dir)
	if oracle2541Has(ask, oracle2541Send) || oracle2541Has(ask, oracle2541Launch) {
		t.Fatalf("permission_ask=false rerun kept managed ask entries: %v", ask)
	}
	if !oracle2541Has(ask, "Bash(git push *)") {
		t.Fatalf("user ask rule lost: %v", ask)
	}
}

// C control: key absent keeps the #1358 ask list (default unchanged).
func TestOracle2541_C_DefaultKeepsAskList(t *testing.T) {
	dir := oracle2541Setup(t, "", "")
	ask, _ := oracle2541Ask(t, dir)
	if !oracle2541Has(ask, oracle2541Send) || !oracle2541Has(ask, oracle2541Launch) {
		t.Fatalf("default setup must write the ask list: %v", ask)
	}
}
