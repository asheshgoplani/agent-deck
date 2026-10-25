package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetToolInlineEnv_InvalidKeyNotExecuted verifies that a [tools.X].env key
// that is not a valid env var name is skipped, so it cannot inject shell
// syntax into the launch prefix, while valid keys are still exported.
func TestGetToolInlineEnv_InvalidKeyNotExecuted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{
			"testtool": {Env: map[string]string{
				"X=1; touch PWNED; Y": "v",
				"GOOD":                "ok",
			}},
		},
		MCPs: make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	prefix := (&Instance{Tool: "testtool"}).getToolInlineEnv()
	if want := "export GOOD='ok'"; prefix != want {
		t.Errorf("getToolInlineEnv() = %q, want %q", prefix, want)
	}

	home := t.TempDir()
	cmd := exec.Command("bash", "-c", prefix+` && printf '%s' "$GOOD"`)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, _ := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(home, "PWNED")); err == nil {
		t.Fatalf("invalid env key executed injected command; output: %q", out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("valid key not exported: output %q", out)
	}
}
