package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Without its agent-deck extension, a pi session's startup is judged only from
// the pane, and a layout the pane patterns do not know (a footer extension,
// #3098) lets the startup watchdog kill a healthy session. The hold then names
// the fix. The hint is set through applyLaunchSettingsFromConfig, the one
// helper every start and restart path calls before spawning.
//
// The extension is looked for where this session's pi loads it from, as
// piSocketLaunch does: the command's own leading PI_CODING_AGENT_DIR, else
// agent-deck's.
func TestIssue3098_PiStartupTimeoutHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string
		// processDir sets agent-deck's own PI_CODING_AGENT_DIR to a temp dir.
		processDir bool
		// commandDir gives the command a leading PI_CODING_AGENT_DIR of its own.
		commandDir bool
		// ext is what is installed where: "", "current", "outdated" or
		// "foreign" in the dir pi loads, or "current-elsewhere" in the default
		// dir only (which a commandDir session does not load).
		ext     string
		ssh     bool
		sandbox bool
		// wantDir names the dir the hint's install command must carry:
		// "process", "command" or "" (no PI_CODING_AGENT_DIR in the command).
		wantHint bool
		wantDir  string
	}{
		{name: "pi without the extension", tool: "pi", wantHint: true},
		{name: "pi without the extension, agent-deck sets the agent dir", tool: "pi", processDir: true, wantHint: true, wantDir: "process"},
		{name: "pi with the extension", tool: "pi", ext: "current"},
		// An outdated extension still sends session_start, which ends startup.
		{name: "pi with an outdated extension", tool: "pi", ext: "outdated"},
		// Somebody else's agent-deck.ts: the hint's install command would refuse.
		{name: "pi with a foreign file", tool: "pi", ext: "foreign"},
		// The command's own agent dir wins over agent-deck's (#3099 review).
		{name: "command agent dir with the extension there", tool: "pi", commandDir: true, ext: "current"},
		{name: "command agent dir, extension only in the default dir", tool: "pi", commandDir: true, ext: "current-elsewhere", wantHint: true, wantDir: "command"},
		{name: "command agent dir overrides agent-deck's", tool: "pi", processDir: true, commandDir: true, wantHint: true, wantDir: "command"},
		// The extension lives on the other host or in the container, which
		// this host cannot inspect.
		{name: "remote pi", tool: "pi", ssh: true},
		{name: "sandboxed pi", tool: "pi", sandbox: true},
		{name: "claude", tool: "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			processDir, commandDir := "", ""
			t.Setenv("PI_CODING_AGENT_DIR", "")
			if tc.processDir {
				processDir = t.TempDir()
				t.Setenv("PI_CODING_AGENT_DIR", processDir)
			}
			loadedDir := PiExtensionsDir()
			inst := NewInstanceWithTool("issue3098", t.TempDir(), tc.tool)
			if tc.commandDir {
				commandDir = t.TempDir()
				inst.Command = "PI_CODING_AGENT_DIR=" + commandDir + " pi"
				loadedDir = filepath.Join(commandDir, "extensions")
			}
			switch tc.ext {
			case "current":
				if _, err := InstallPiHooks(loadedDir); err != nil {
					t.Fatal(err)
				}
			case "current-elsewhere":
				if _, err := InstallPiHooks(filepath.Join(home, ".pi", "agent", "extensions")); err != nil {
					t.Fatal(err)
				}
			case "outdated":
				writePiExtensionForTest(t, loadedDir, "// "+piHookExtensionMarker+" v1\n")
			case "foreign":
				writePiExtensionForTest(t, loadedDir, "// my own extension\n")
			}
			if tc.ssh {
				inst.SSHHost = "remote-host"
			}
			if tc.sandbox {
				inst.Sandbox = &SandboxConfig{Enabled: true}
			}
			inst.applyLaunchSettingsFromConfig()

			hint := inst.tmuxSession.StartupTimeoutHint()
			if !tc.wantHint {
				if hint != "" {
					t.Fatalf("unexpected hint %q", hint)
				}
				return
			}
			want := "agent-deck pi-hooks install"
			switch tc.wantDir {
			case "process":
				want = "PI_CODING_AGENT_DIR=" + processDir + " " + want
			case "command":
				want = "PI_CODING_AGENT_DIR=" + commandDir + " " + want
			}
			if !strings.Contains(hint, "'"+want+"'") {
				t.Fatalf("hint must name the install command %q, got %q", want, hint)
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
