package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Without its agent-deck extension a pi session's status and startup are read
// from the pane, and a layout the pane patterns do not know can get a healthy
// session killed at startup (#3098). That state must be visible: `doctor` and
// `session show` say whether the extension is installed and how to fix it.

func TestPiHooksStateForDir(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string // written to agent-deck.ts unless skip
		skip        bool
		wantState   string
		wantVersion int
	}{
		{name: "no extension", skip: true, wantState: piHooksNotInstalled},
		{name: "current extension", content: session.PiHookExtensionSource(), wantState: piHooksInstalled, wantVersion: session.PiHookExtensionVersion()},
		{name: "older extension", content: "// AGENTDECK PI HOOK EXTENSION v1\n", wantState: piHooksOutdated, wantVersion: 1},
		{name: "foreign file", content: "// my own agent-deck.ts\n", wantState: piHooksForeign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.skip {
				if err := os.WriteFile(session.PiHookExtensionPath(dir), []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := piHooksStateForDir(dir)
			if got.State != tc.wantState || got.Version != tc.wantVersion {
				t.Fatalf("state = %+v, want state %q version %d", got, tc.wantState, tc.wantVersion)
			}
			if want := session.PiHookExtensionPath(dir); got.Extension != want {
				t.Fatalf("extension path = %q, want %q", got.Extension, want)
			}
		})
	}
}

func TestPiHooksStateForInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agentDir := filepath.Join(home, "pi-agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	local := session.NewInstanceWithTool("pi-local", home, "pi")
	got := piHooksStateForInstance(local)
	if got.State != piHooksNotInstalled {
		t.Fatalf("local pi without the extension: state = %q, want %q", got.State, piHooksNotInstalled)
	}
	if want := filepath.Join(agentDir, "extensions", "agent-deck.ts"); got.Extension != want {
		t.Fatalf("extension path = %q, want %q", got.Extension, want)
	}

	remote := session.NewInstanceWithTool("pi-remote", home, "pi")
	remote.SSHHost = "remote-host"
	if got := piHooksStateForInstance(remote); got.State != piHooksUnknown || got.Extension != "" {
		t.Fatalf("remote pi: %+v, want state %q and no path (the extension is on the remote host)", got, piHooksUnknown)
	}

	sandboxed := session.NewInstanceWithTool("pi-sandboxed", home, "pi")
	sandboxed.Sandbox = &session.SandboxConfig{Enabled: true}
	if got := piHooksStateForInstance(sandboxed); got.State != piHooksUnknown {
		t.Fatalf("sandboxed pi: state = %q, want %q", got.State, piHooksUnknown)
	}

	claude := session.NewInstanceWithTool("claude", home, "claude")
	if got := piHooksStateForInstance(claude); got.State != "" {
		t.Fatalf("non-pi tool: state = %q, want empty", got.State)
	}
}

func TestPiHooksLine(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	const ext = "/x/extensions/agent-deck.ts"
	current := session.PiHookExtensionVersion()
	for _, tc := range []struct {
		name  string
		state piHooksReport
		want  []string
	}{
		{"installed", piHooksReport{State: piHooksInstalled, Version: current, Extension: ext},
			[]string{"extension installed", ext}},
		{"not installed", piHooksReport{State: piHooksNotInstalled, Extension: ext},
			[]string{"extension not installed (pane detection only)", "agent-deck pi-hooks install"}},
		{"outdated", piHooksReport{State: piHooksOutdated, Version: 1, Extension: ext},
			[]string{"extension outdated (v1", "agent-deck pi-hooks install"}},
		{"foreign", piHooksReport{State: piHooksForeign, Extension: ext},
			[]string{"extension blocked", "not written by agent-deck", ext}},
		{"unknown", piHooksReport{State: piHooksUnknown},
			[]string{"extension unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := piHooksLine(tc.state)
			if !strings.HasPrefix(got, tc.want[0]) || !containsAll(got, tc.want...) {
				t.Fatalf("piHooksLine(%+v) = %q, want prefix %q containing %q", tc.state, got, tc.want[0], tc.want[1:])
			}
		})
	}
}

// Like the codex line's CODEX_HOME, a custom PI_CODING_AGENT_DIR is part of
// the command: run from a shell without it, a bare install would write to
// ~/.pi/agent, which this pi does not load.
func TestPiHooksLineNamesCustomAgentDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/custom dir/pi")
	for _, state := range []string{piHooksNotInstalled, piHooksOutdated, piHooksForeign} {
		got := piHooksLine(piHooksReport{State: state, Version: 1, Extension: "/custom dir/pi/extensions/agent-deck.ts"})
		if want := "PI_CODING_AGENT_DIR='/custom dir/pi' agent-deck pi-hooks install"; !strings.Contains(got, want) {
			t.Fatalf("%s line = %q, want it to contain %q", state, got, want)
		}
	}
}
