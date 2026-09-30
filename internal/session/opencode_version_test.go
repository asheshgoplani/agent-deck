package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinOpenCodeMajorVersion(t *testing.T, major int, ok bool) {
	t.Helper()
	prev := probeOpenCodeMajorVersion
	probeOpenCodeMajorVersion = func(*Instance) (int, bool) { return major, ok }
	t.Cleanup(func() { probeOpenCodeMajorVersion = prev })
}

// useInstalledOpenCodeProbe undoes TestMain's pin so a test exercises the real
// resolve-and-probe path against the stub binaries it wrote.
func useInstalledOpenCodeProbe(t *testing.T) {
	t.Helper()
	prev := probeOpenCodeMajorVersion
	probeOpenCodeMajorVersion = probeInstalledOpenCodeMajorVersion
	openCodeVersionMemo.Clear()
	t.Cleanup(func() {
		probeOpenCodeMajorVersion = prev
		openCodeVersionMemo.Clear()
	})
}

// writeOpenCodeStub writes an executable `opencode` into dir that prints
// version and appends a line to dir/calls on every run.
func writeOpenCodeStub(t *testing.T, dir, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir stub dir: %v", err)
	}
	path := filepath.Join(dir, "opencode")
	stub := "#!/bin/sh\necho call >> '" + filepath.Join(dir, "calls") + "'\necho '" + version + "'\n"
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("write opencode stub: %v", err)
	}
	return path
}

// isolateOpenCodeConfig gives the test its own HOME with [opencode].command
// set to command (literal TOML string), so no other test's config leaks in.
func isolateOpenCodeConfig(t *testing.T, command string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	configPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[opencode]\ncommand = '"+command+"'\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
}

// newOpenCodeResumeInstance is a resumed session carrying every flag that
// buildOpenCodeCommand can add: -s, -m, --agent and (from the stale port) --port.
func newOpenCodeResumeInstance(t *testing.T) *Instance {
	t.Helper()
	inst := &Instance{Tool: "opencode", OpenCodeSessionID: "ses_ABC123", OpenCodePort: 4242}
	if err := inst.SetOpenCodeOptions(&OpenCodeOptions{Model: "openai/gpt-5.5", Agent: "build"}); err != nil {
		t.Fatalf("SetOpenCodeOptions: %v", err)
	}
	return inst
}

func TestParseOpenCodeMajorVersion(t *testing.T) {
	tests := []struct {
		out       string
		wantMajor int
		wantOK    bool
	}{
		{out: "opencode v2.0.20\n", wantMajor: 2, wantOK: true},
		{out: "1.14.3\n", wantMajor: 1, wantOK: true},
		{out: "0.15.8", wantMajor: 0, wantOK: true},
		{out: "", wantOK: false},
		{out: "opencode dev", wantOK: false},
	}
	for _, tt := range tests {
		major, ok := parseOpenCodeMajorVersion(tt.out)
		if major != tt.wantMajor || ok != tt.wantOK {
			t.Errorf("parseOpenCodeMajorVersion(%q) = (%d, %v), want (%d, %v)",
				tt.out, major, ok, tt.wantMajor, tt.wantOK)
		}
	}
}

// TestBuildOpenCodeCommand_V2OmitsRejectedFlags: OpenCode 2.x exits with
// "Unrecognized flag" on -m, --agent and --port, so a launch carrying any of
// them dies before the TUI draws (spawn_died_fast).
func TestBuildOpenCodeCommand_V2OmitsRejectedFlags(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	inst := newOpenCodeResumeInstance(t)

	cmd := inst.buildOpenCodeCommand("opencode")
	for _, flag := range []string{" -m ", " --agent ", " --port "} {
		if strings.Contains(cmd, flag) {
			t.Errorf("2.x launch must not carry %q: %q", strings.TrimSpace(flag), cmd)
		}
	}
	if !strings.Contains(cmd, "opencode -s ses_ABC123") {
		t.Errorf("2.x launch must still resume with -s: %q", cmd)
	}
	if port := inst.GetOpenCodePort(); port != 0 {
		t.Errorf("2.x launch binds no SSE server, so the stale port must be cleared, got %d", port)
	}
}

func TestBuildOpenCodeCommand_V1AndUnknownKeepFlags(t *testing.T) {
	tests := []struct {
		name  string
		major int
		ok    bool
	}{
		{name: "1.x", major: 1, ok: true},
		{name: "unknown version", major: 0, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinOpenCodeMajorVersion(t, tt.major, tt.ok)
			inst := newOpenCodeResumeInstance(t)

			cmd := inst.buildOpenCodeCommand("opencode")
			for _, want := range []string{"opencode -s ses_ABC123", " -m openai/gpt-5.5", " --agent build", " --port "} {
				if !strings.Contains(cmd, want) {
					t.Errorf("launch missing %q: %q", want, cmd)
				}
			}
		})
	}
}

// TestBuildOpenCodeCommand_RemoteKeepsV1Flags: sandboxed and SSH sessions run
// a binary this host cannot probe, so a local 2.x must not change their flags.
func TestBuildOpenCodeCommand_RemoteKeepsV1Flags(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	tests := map[string]*Instance{
		"ssh":     {Tool: "opencode", SSHHost: "devbox"},
		"sandbox": {Tool: "opencode", Sandbox: &SandboxConfig{Enabled: true}},
	}
	for name, inst := range tests {
		t.Run(name, func(t *testing.T) {
			if cmd := inst.buildOpenCodeCommand("opencode"); !strings.Contains(cmd, " --port ") {
				t.Errorf("%s launch lost its 1.x flags: %q", name, cmd)
			}
		})
	}
}

// TestBuildOpenCodeCommand_ConfiguredPATHDecidesVersion: when the configured
// command carries its own PATH=, the version that PATH resolves decides the
// flags, not whichever opencode this process's PATH finds first.
func TestBuildOpenCodeCommand_ConfiguredPATHDecidesVersion(t *testing.T) {
	root := t.TempDir()
	v1Dir, v2Dir := filepath.Join(root, "v1"), filepath.Join(root, "v2")
	writeOpenCodeStub(t, v1Dir, "1.14.3")
	writeOpenCodeStub(t, v2Dir, "opencode v2.0.20")

	tests := []struct {
		name        string
		processDir  string
		commandDir  string
		wantV1Flags bool
	}{
		{name: "process finds 2.x, command runs 1.x", processDir: v2Dir, commandDir: v1Dir, wantV1Flags: true},
		{name: "process finds 1.x, command runs 2.x", processDir: v1Dir, commandDir: v2Dir, wantV1Flags: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateOpenCodeConfig(t, "PATH="+tt.commandDir+":$PATH opencode")
			t.Setenv("PATH", tt.processDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			useInstalledOpenCodeProbe(t)

			cmd := newOpenCodeResumeInstance(t).buildOpenCodeCommand("opencode")
			if got := strings.Contains(cmd, " --port ") && strings.Contains(cmd, " -m openai/gpt-5.5"); got != tt.wantV1Flags {
				t.Errorf("1.x flags present = %v, want %v: %q", got, tt.wantV1Flags, cmd)
			}
		})
	}
}

// TestOpenCodeLaunchBinary_Resolution covers the launch-parity cases the probe
// must follow: a quoted configured path with spaces, and a binary reachable
// only through the spawn-path prelude's ~/.local/bin.
func TestOpenCodeLaunchBinary_Resolution(t *testing.T) {
	t.Run("quoted path with spaces", func(t *testing.T) {
		want := writeOpenCodeStub(t, filepath.Join(t.TempDir(), "My Tools"), "opencode v2.0.20")
		isolateOpenCodeConfig(t, `"`+want+`"`)

		got, ok := (&Instance{Tool: "opencode"}).openCodeLaunchBinary()
		if !ok || got != want {
			t.Fatalf("openCodeLaunchBinary() = (%q, %v), want (%q, true)", got, ok, want)
		}
	})

	t.Run("env wrapper", func(t *testing.T) {
		want := writeOpenCodeStub(t, t.TempDir(), "opencode v2.0.20")
		t.Setenv("PATH", t.TempDir())
		for _, command := range []string{
			"env PATH=" + filepath.Dir(want) + ":$PATH opencode",
			"PATH=" + filepath.Dir(want) + ":$PATH /usr/bin/env opencode",
		} {
			isolateOpenCodeConfig(t, command)
			got, ok := (&Instance{Tool: "opencode"}).openCodeLaunchBinary()
			if !ok || got != want {
				t.Errorf("%s: openCodeLaunchBinary() = (%q, %v), want (%q, true)", command, got, ok, want)
			}
		}

		isolateOpenCodeConfig(t, "env -u FOO opencode")
		if got, ok := (&Instance{Tool: "opencode"}).openCodeLaunchBinary(); ok {
			t.Errorf("env with options must stay unknown, got %q", got)
		}
	})

	t.Run("spawn-path prelude dir", func(t *testing.T) {
		isolateOpenCodeConfig(t, "opencode")
		want := writeOpenCodeStub(t, filepath.Join(os.Getenv("HOME"), ".local", "bin"), "opencode v2.0.20")
		t.Setenv("PATH", t.TempDir())

		got, ok := (&Instance{Tool: "opencode"}).openCodeLaunchBinary()
		if !ok || got != want {
			t.Fatalf("openCodeLaunchBinary() = (%q, %v), want (%q, true)", got, ok, want)
		}
	})
}

// TestProbeOpenCodeBinaryMajorVersion runs the real probe against a stub
// binary and checks that a successful answer is memoised.
func TestProbeOpenCodeBinaryMajorVersion(t *testing.T) {
	dir := t.TempDir()
	stub := writeOpenCodeStub(t, dir, "opencode v2.0.20")
	openCodeVersionMemo.Clear()
	t.Cleanup(openCodeVersionMemo.Clear)

	for n := 0; n < 2; n++ {
		major, ok := probeOpenCodeBinaryMajorVersion(stub)
		if major != 2 || !ok {
			t.Fatalf("probe #%d = (%d, %v), want (2, true)", n+1, major, ok)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatalf("read stub call log: %v", err)
	}
	if got := strings.Count(string(data), "call"); got != 1 {
		t.Errorf("stub ran %d times, want 1 (memoised)", got)
	}
}
