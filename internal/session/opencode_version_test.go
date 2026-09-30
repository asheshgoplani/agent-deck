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
	probeOpenCodeMajorVersion = func() (int, bool) { return major, ok }
	t.Cleanup(func() { probeOpenCodeMajorVersion = prev })
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

// TestProbeInstalledOpenCodeMajorVersion runs the real probe against a stub
// binary and checks that a successful answer is memoised.
func TestProbeInstalledOpenCodeMajorVersion(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	stub := "#!/bin/sh\necho call >> '" + calls + "'\necho 'opencode v2.0.20'\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write opencode stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	openCodeVersionMemo.Clear()
	t.Cleanup(openCodeVersionMemo.Clear)

	for n := 0; n < 2; n++ {
		major, ok := probeInstalledOpenCodeMajorVersion()
		if major != 2 || !ok {
			t.Fatalf("probe #%d = (%d, %v), want (2, true)", n+1, major, ok)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read stub call log: %v", err)
	}
	if got := strings.Count(string(data), "call"); got != 1 {
		t.Errorf("stub ran %d times, want 1 (memoised)", got)
	}
}
