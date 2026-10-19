package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

// TestToolDirResolversRefuseTheRealHome points each tool config dir resolver at
// the real home, the way an un-isolated test or a leaked override env var does,
// and requires the resolver to panic before returning the path. Only path
// strings are computed: the panic fires before anything reads the directory.
func TestToolDirResolversRefuseTheRealHome(t *testing.T) {
	homes := sandboxguard.RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	real := homes[0]

	cases := []struct {
		name string
		env  map[string]string
		fn   func() string
	}{
		{"CLAUDE_CONFIG_DIR", map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(real, ".claude")}, GetClaudeConfigDir},
		{"CODEX_HOME", map[string]string{"CODEX_HOME": filepath.Join(real, ".codex")}, GetCodexConfigDir},
		{"COPILOT_CONFIG_DIR", map[string]string{"COPILOT_CONFIG_DIR": filepath.Join(real, ".copilot")}, getCopilotHomeDir},
		{"DSH_HOME", map[string]string{"DSH_HOME": filepath.Join(real, ".dsh")}, getDeepSeekHomeDir},
		{"OpenCode via HOME", map[string]string{"HOME": real, "XDG_CONFIG_HOME": ""}, GetOpenCodeConfigDir},
		{"Gemini via HOME", map[string]string{"HOME": real}, GetGeminiConfigDir},
		{"Cursor via HOME", map[string]string{"HOME": real}, GetCursorConfigDir},
		{"Hermes via HOME", map[string]string{"HOME": real}, GetHermesConfigDir},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			got, msg := callRecover(c.fn)
			if msg == "" {
				t.Fatalf("resolver returned %q inside the real home %q without panicking", got, real)
			}
			if !strings.Contains(msg, "IsolateHome") {
				t.Fatalf("panic message does not say how to fix it: %s", msg)
			}
		})
	}
}

// TestToolDirResolversAllowTheSandbox is the other half: the same resolvers
// pointed at a t.TempDir() return the path unchanged.
func TestToolDirResolversAllowTheSandbox(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex"))
	if got, msg := callRecover(GetClaudeConfigDir); msg != "" || got != filepath.Join(tmp, "claude") {
		t.Fatalf("GetClaudeConfigDir = %q, panic %q", got, msg)
	}
	if got, msg := callRecover(GetCodexConfigDir); msg != "" || got != filepath.Join(tmp, "codex") {
		t.Fatalf("GetCodexConfigDir = %q, panic %q", got, msg)
	}
	if got, msg := callRecover(GetGeminiConfigDir); msg != "" || got == "" {
		t.Fatalf("GetGeminiConfigDir = %q, panic %q", got, msg)
	}
}

func callRecover(fn func() string) (got, panicMsg string) {
	defer func() {
		if r := recover(); r != nil {
			panicMsg = toString(r)
		}
	}()
	return fn(), ""
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "non-string panic"
}
