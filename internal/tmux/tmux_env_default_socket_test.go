package tmux

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A session with no socket name lives on the default server. Inside a pane on
// a [tmux.binaries] socket, $TMUX names that socket, and a socket-less tmux
// call followed it: `session send` from a deck36 pane to a default-server
// session said "not running".
func TestTmuxArgs_DefaultSessionInsideConfiguredSocketPane(t *testing.T) {
	SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { SetSocketBinaries(nil) })

	cases := []struct {
		name   string
		tmux   string
		socket string
		want   []string
	}{
		{"inside a configured socket pane, default session", "/tmp/tmux-1000/deck36,123,0", "", []string{"-u", "-L", "default", "has-session", "-t", "s"}},
		{"inside a configured socket pane, session on that socket", "/tmp/tmux-1000/deck36,123,0", "deck36", []string{"-u", "-L", "deck36", "has-session", "-t", "s"}},
		{"inside the default server", "/tmp/tmux-1000/default,123,0", "", []string{"-u", "has-session", "-t", "s"}},
		{"inside a private server without an entry keeps $TMUX routing", "/tmp/tmux-1000/mine,123,0", "", []string{"-u", "has-session", "-t", "s"}},
		{"outside tmux", "", "", []string{"-u", "has-session", "-t", "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX", tc.tmux)
			got := tmuxArgs(tc.socket, "has-session", "-t", "s")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("tmuxArgs(%q) with TMUX=%q = %q, want %q", tc.socket, tc.tmux, got, tc.want)
			}
		})
	}
}

// The same bug through start and attach: `session start` from a deck36 pane
// created a default-server session on deck36 (its record has no socket, so its
// status read error), and attach went to the wrong server.
func TestStartAndAttach_DefaultSessionInsideConfiguredSocketPane(t *testing.T) {
	SetSocketBinaries(map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})
	t.Cleanup(func() { SetSocketBinaries(nil) })
	pinInitialWindowSize(t, 120, 40, true)
	t.Setenv("TMUX", "/tmp/tmux-1000/deck36,123,0")
	t.Setenv("TMUX_PANE", "%3")

	for _, mode := range []string{"direct", "scope", "service"} {
		t.Run("start/"+mode, func(t *testing.T) {
			s := &Session{Name: "agentdeck_def_1234abcd", WorkDir: "/tmp/project", LaunchAs: mode}
			launcher, args := s.startCommandSpec("/tmp/project", "")
			if mode == "direct" {
				if launcher != "tmux" {
					t.Fatalf("launcher = %q, want the PATH tmux for the default server", launcher)
				}
			}
			inner := stripSystemdRunPrefix(args, "tmux")
			want := []string{"-u", "-L", "default", "new-session", "-d", "-s", "agentdeck_def_1234abcd",
				"-c", "/tmp/project", "-x", "120", "-y", "40"}
			if !slices.Equal(inner, want) {
				t.Fatalf("new-session argv = %q, want %q", inner, want)
			}
		})
	}

	t.Run("attach", func(t *testing.T) {
		s := &Session{Name: "agentdeck_def_1234abcd"}
		cmd := s.attachCmd(t.Context())
		want := []string{"-u", "-L", "default", "attach-session", "-t", "agentdeck_def_1234abcd"}
		if !slices.Equal(cmd.Args[1:], want) {
			t.Fatalf("attach argv = %q, want %q", cmd.Args[1:], want)
		}
	})
}

// Inside a pane on a configured socket, PATH starts with that socket's binary
// directory, so the bare "tmux" is the newer client. The default server must
// still get the PATH tmux outside those directories: a newer client fails
// against an older default server, and with no default server running it
// would start one as the newer version.
func TestDefaultBinary_SkipsConfiguredBinaryDirs(t *testing.T) {
	newDir, oldDir := t.TempDir(), t.TempDir()
	for _, d := range []string{newDir, oldDir} {
		if err := os.WriteFile(filepath.Join(d, "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", newDir+string(os.PathListSeparator)+oldDir)

	SetSocketBinaries(nil)
	if got := BinaryFor(""); got != "tmux" {
		t.Fatalf("without [tmux.binaries], BinaryFor(\"\") = %q, want \"tmux\"", got)
	}

	SetSocketBinaries(map[string]string{"deck36": filepath.Join(newDir, "tmux")})
	t.Cleanup(func() { SetSocketBinaries(nil) })
	want := filepath.Join(oldDir, "tmux")
	for _, socket := range []string{"", "default", "other"} {
		if got := BinaryFor(socket); got != want {
			t.Errorf("BinaryFor(%q) = %q, want %q (PATH tmux outside the configured binary's dir)", socket, got, want)
		}
	}
	if got := BinaryFor("deck36"); got != filepath.Join(newDir, "tmux") {
		t.Errorf("BinaryFor(deck36) = %q, want the configured binary", got)
	}
	if got := BinaryForSocketPath(""); got != want {
		t.Errorf("BinaryForSocketPath(\"\") = %q, want %q", got, want)
	}
	if !isDirectTmuxLauncher("", want) {
		t.Errorf("the resolved default binary must count as a direct tmux launcher")
	}

	// PATH whose first tmux is not in a configured dir: plain "tmux", as before.
	t.Setenv("PATH", oldDir+string(os.PathListSeparator)+newDir)
	if got := BinaryFor(""); got != "tmux" {
		t.Errorf("first PATH tmux outside configured dirs: BinaryFor(\"\") = %q, want \"tmux\"", got)
	}
}
