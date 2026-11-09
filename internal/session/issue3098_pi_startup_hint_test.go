package session

import (
	"os"
	"strings"
	"testing"
)

// Without its agent-deck extension, a pi session's startup is judged only from
// the pane, and a layout the pane patterns do not know (a footer extension,
// #3098) lets the startup watchdog kill a healthy session. The hold then names
// the fix. The hint is set through applyLaunchSettingsFromConfig, the one
// helper every start and restart path calls before spawning.
func TestIssue3098_PiStartupTimeoutHint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		ext      string // "", "current", "outdated" or "foreign"
		ssh      bool
		sandbox  bool
		wantHint bool
	}{
		{name: "pi without the extension", tool: "pi", wantHint: true},
		{name: "pi with the extension", tool: "pi", ext: "current"},
		// An outdated extension still sends session_start, which ends startup.
		{name: "pi with an outdated extension", tool: "pi", ext: "outdated"},
		// Somebody else's agent-deck.ts: the hint's install command would refuse.
		{name: "pi with a foreign file", tool: "pi", ext: "foreign"},
		// The extension lives on the other host or in the container, which
		// this host cannot inspect.
		{name: "remote pi", tool: "pi", ssh: true},
		{name: "sandboxed pi", tool: "pi", sandbox: true},
		{name: "claude", tool: "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedHomeDir(t)
			dir := t.TempDir()
			t.Setenv("PI_CODING_AGENT_DIR", dir)
			extDir := PiExtensionsDir()
			switch tc.ext {
			case "current":
				if _, err := InstallPiHooks(extDir); err != nil {
					t.Fatal(err)
				}
			case "outdated":
				writePiExtensionForTest(t, extDir, "// "+piHookExtensionMarker+" v1\n")
			case "foreign":
				writePiExtensionForTest(t, extDir, "// my own extension\n")
			}

			inst := NewInstanceWithTool("issue3098", t.TempDir(), tc.tool)
			if tc.ssh {
				inst.SSHHost = "remote-host"
			}
			if tc.sandbox {
				inst.Sandbox = &SandboxConfig{Enabled: true}
			}
			inst.applyLaunchSettingsFromConfig()

			hint := inst.tmuxSession.StartupTimeoutHint()
			if tc.wantHint {
				// PI_CODING_AGENT_DIR is set here, so the command names it.
				if want := "PI_CODING_AGENT_DIR=" + dir + " agent-deck pi-hooks install"; !strings.Contains(hint, want) {
					t.Fatalf("hint must name the install command %q, got %q", want, hint)
				}
				return
			}
			if hint != "" {
				t.Fatalf("unexpected hint %q", hint)
			}
		})
	}
}

// Installing the extension and restarting clears the hint: the restart path
// recomputes it rather than keeping the first spawn's verdict.
func TestIssue3098_PiStartupTimeoutHintClearsAfterInstall(t *testing.T) {
	isolatedHomeDir(t)
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	inst := NewInstanceWithTool("issue3098-install", t.TempDir(), "pi")

	inst.applyLaunchSettingsFromConfig()
	if inst.tmuxSession.StartupTimeoutHint() == "" {
		t.Fatal("precondition: no hint before the extension is installed")
	}
	if _, err := InstallPiHooks(PiExtensionsDir()); err != nil {
		t.Fatal(err)
	}
	inst.applyLaunchSettingsFromConfig()
	if hint := inst.tmuxSession.StartupTimeoutHint(); hint != "" {
		t.Fatalf("hint survived installing the extension: %q", hint)
	}
}

func writePiExtensionForTest(t *testing.T, extDir, content string) {
	t.Helper()
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PiHookExtensionPath(extDir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
