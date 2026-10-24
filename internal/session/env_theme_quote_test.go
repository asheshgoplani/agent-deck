package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A parent COLORFGBG that matches the theme is propagated verbatim; a quote in
// it must not break out of the export and run shell.
func TestThemeEnvExport_ParentValueNotShellEvaluated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCache = &UserConfig{Theme: "light"}
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	})
	runDir := t.TempDir()
	evil := "a'$(touch PWNED)';15"
	t.Setenv("COLORFGBG", evil)

	export := themeEnvExport()
	cmd := exec.Command("bash", "-c", export+` && printf %s "$COLORFGBG"`)
	cmd.Dir = runDir
	out, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(filepath.Join(runDir, "PWNED")); statErr == nil {
		t.Errorf("injected command executed: PWNED created\nexport: %s", export)
	}
	if err != nil {
		t.Fatalf("export failed: %v (%s)\nexport: %s", err, out, export)
	}
	if got := string(out); got != evil && got != "0;15" && got != "15;0" {
		t.Errorf("COLORFGBG = %q, want the parent value or a synthetic default", got)
	}
}
