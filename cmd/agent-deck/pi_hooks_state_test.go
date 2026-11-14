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

func TestPiHooksStateForAgentDir(t *testing.T) {
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
			agentDir := t.TempDir()
			extDir := filepath.Join(agentDir, "extensions")
			if !tc.skip {
				if err := os.MkdirAll(extDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(session.PiHookExtensionPath(extDir), []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := piHooksStateForAgentDir(agentDir)
			if got.State != tc.wantState || got.Version != tc.wantVersion {
				t.Fatalf("state = %+v, want state %q version %d", got, tc.wantState, tc.wantVersion)
			}
			if want := session.PiHookExtensionPath(extDir); got.Extension != want {
				t.Fatalf("extension path = %q, want %q", got.Extension, want)
			}
			if got.AgentDir != agentDir {
				t.Fatalf("agent dir = %q, want %q", got.AgentDir, agentDir)
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

// A pi command with its own leading PI_CODING_AGENT_DIR loads extensions from
// there, not from agent-deck's dir, so `session show` reports that dir and
// names it in the install command (#3099 review).
func TestPiHooksStateForInstanceUsesTheCommandAgentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if _, err := session.InstallPiHooks(filepath.Join(home, ".pi", "agent", "extensions")); err != nil {
		t.Fatal(err)
	}

	cmdDir := filepath.Join(home, "other-agent")
	inst := session.NewInstanceWithTool("pi-cmd-dir", home, "pi")
	inst.Command = "PI_CODING_AGENT_DIR=" + cmdDir + " pi"
	got := piHooksStateForInstance(inst)
	if got.State != piHooksNotInstalled {
		t.Fatalf("command agent dir without the extension: state = %q, want %q (the default dir's extension is not the one this pi loads)", got.State, piHooksNotInstalled)
	}
	if want := filepath.Join(cmdDir, "extensions", "agent-deck.ts"); got.Extension != want {
		t.Fatalf("extension path = %q, want %q", got.Extension, want)
	}
	if line, want := piHooksLine(got), "PI_CODING_AGENT_DIR="+cmdDir+" agent-deck pi-hooks install"; !strings.Contains(line, want) {
		t.Fatalf("line = %q, want it to contain %q", line, want)
	}

	if _, err := session.InstallPiHooks(filepath.Join(cmdDir, "extensions")); err != nil {
		t.Fatal(err)
	}
	if got := piHooksStateForInstance(inst); got.State != piHooksInstalled {
		t.Fatalf("command agent dir with the extension: state = %q, want %q", got.State, piHooksInstalled)
	}
}

// A pi command that clears agent-deck's PI_CODING_AGENT_DIR with an empty
// assignment loads pi's default dir. A plain install run from a shell with the
// inherited dir would write to that dir instead, so `session show` names the
// empty assignment (#3098).
func TestPiHooksStateForInstanceCommandClearsTheAgentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "inherited"))

	inst := session.NewInstanceWithTool("pi-cmd-empty-dir", home, "pi")
	inst.Command = "PI_CODING_AGENT_DIR= pi"
	got := piHooksStateForInstance(inst)
	if got.State != piHooksNotInstalled {
		t.Fatalf("state = %q, want %q", got.State, piHooksNotInstalled)
	}
	if want := filepath.Join(home, ".pi", "agent", "extensions", "agent-deck.ts"); got.Extension != want {
		t.Fatalf("extension path = %q, want %q (pi's default dir)", got.Extension, want)
	}
	if line, want := piHooksLine(got), "run 'PI_CODING_AGENT_DIR= agent-deck pi-hooks install'"; !strings.Contains(line, want) {
		t.Fatalf("line = %q, want it to contain %q", line, want)
	}
}

func TestPiHooksLine(t *testing.T) {
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
	for _, state := range []string{piHooksNotInstalled, piHooksOutdated, piHooksForeign} {
		got := piHooksLine(piHooksReport{State: state, Version: 1, Extension: "/custom dir/pi/extensions/agent-deck.ts", AgentDir: "/custom dir/pi"})
		if want := "PI_CODING_AGENT_DIR='/custom dir/pi' agent-deck pi-hooks install"; !strings.Contains(got, want) {
			t.Fatalf("%s line = %q, want it to contain %q", state, got, want)
		}
	}
}
