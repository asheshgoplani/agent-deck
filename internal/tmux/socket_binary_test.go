package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setSocketBinariesForTest installs m for one test and clears it after.
func setSocketBinariesForTest(t *testing.T, m map[string]string) {
	t.Helper()
	SetSocketBinaries(m)
	t.Cleanup(func() { SetSocketBinaries(nil) })
}

// fakeTmuxBinary writes an executable that answers `-V` with "tmux <ver>".
func fakeTmuxBinary(t *testing.T, ver string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho 'tmux "+ver+"'\n"), 0o755)) // #nosec G306 -- test helper must be executable
	return path
}

func TestBinaryFor_PerSocket(t *testing.T) {
	setSocketBinariesForTest(t, map[string]string{
		" deck36 ": " /opt/tmux-3.6/bin/tmux ",
		"blank":    "  ",
		"":         "/ignored/tmux",
	})
	cases := []struct{ socket, want string }{
		{"deck36", "/opt/tmux-3.6/bin/tmux"},
		{" deck36", "/opt/tmux-3.6/bin/tmux"},
		{"other", "tmux"},
		{"", "tmux"},
		{"blank", "tmux"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, BinaryFor(c.socket), "socket %q", c.socket)
	}
	assert.Equal(t, "/opt/tmux-3.6/bin/tmux", BinaryForSocketPath("/tmp/tmux-1000/deck36"))
	assert.Equal(t, "tmux", BinaryForSocketPath("/tmp/tmux-1000/default"))
	assert.Equal(t, "tmux", BinaryForSocketPath(""))
}

func TestTmuxExec_RunsTheSocketsBinary(t *testing.T) {
	setSocketBinariesForTest(t, map[string]string{"deck36": "/opt/tmux-3.6/bin/tmux"})

	cmd := tmuxExec("deck36", "list-sessions")
	assert.Equal(t, "/opt/tmux-3.6/bin/tmux", cmd.Path)
	assert.Equal(t, []string{"/opt/tmux-3.6/bin/tmux", "-u", "-L", "deck36", "list-sessions"}, cmd.Args)

	cmd = tmuxExecContext(t.Context(), "deck36", "-u", "-C", "attach-session", "-t", "x")
	assert.Equal(t, "/opt/tmux-3.6/bin/tmux", cmd.Path, "control-mode pipes use the socket's binary")

	s := &Session{Name: "x", SocketName: "deck36"}
	assert.Equal(t, "/opt/tmux-3.6/bin/tmux", s.tmuxCmd("-u", "attach-session", "-t", "x").Path, "attach uses the socket's binary")

	assert.Equal(t, "tmux", tmuxExec("other", "list-sessions").Args[0], "a socket without an entry keeps PATH tmux")
}

func TestStartCommandSpec_RunsTheSocketsBinary(t *testing.T) {
	const bin = "/opt/tmux-3.6/bin/tmux"
	setSocketBinariesForTest(t, map[string]string{"deck36": bin})
	pinInitialWindowSize(t, 120, 40, true)

	for _, mode := range []string{"direct", "scope", "service"} {
		t.Run(mode, func(t *testing.T) {
			s := &Session{Name: "agentdeck_bin_1234abcd", WorkDir: "/tmp/project", LaunchAs: mode, SocketName: "deck36"}
			launcher, args := s.startCommandSpec("/tmp/project", "")
			if mode == "direct" {
				assert.Equal(t, bin, launcher)
			} else {
				require.Equal(t, "systemd-run", launcher)
				assert.Contains(t, args, bin, "systemd-run must exec the socket's binary")
				assert.NotContains(t, args, "tmux")
			}
			inner := stripSystemdRunPrefix(args, bin)
			assert.Equal(t, []string{"-u", "-L", "deck36", "new-session", "-d", "-s", "agentdeck_bin_1234abcd",
				"-c", "/tmp/project", "-x", "120", "-y", "40"}, inner)
		})
	}

	scope := buildScopeArgsFromTmuxArgs("agentdeck_bin_1234abcd", bin, []string{"-u", "new-session"})
	assert.Contains(t, scope, bin, "the service-to-scope fallback execs the socket's binary")
	assert.Equal(t, []string{"-u", "new-session"}, stripSystemdRunPrefix(scope, bin))
}

// The codepoint-widths gate must read the version of the binary that serves
// the session's socket, not the host's PATH tmux: on a host with tmux 3.4 on
// PATH and 3.6a configured for the socket, the Indic widths must be applied,
// and the reverse must not apply them. Each half fails on one kind of host
// when the gate reads the host version.
func TestIndicZeroWidthMarks_GateReadsTheSocketsBinary(t *testing.T) {
	setSocketBinariesForTest(t, map[string]string{
		"new36": fakeTmuxBinary(t, "3.6a"),
		"old33": fakeTmuxBinary(t, "3.3"),
	})

	on36 := &Session{Name: "a", SocketName: "new36"}
	on36.SetIndicZeroWidthMarks(true)
	assert.NotEmpty(t, on36.indicZeroWidthMarksArgs(), "tmux 3.6a on the socket supports codepoint-widths")

	on33 := &Session{Name: "b", SocketName: "old33"}
	on33.SetIndicZeroWidthMarks(true)
	assert.Empty(t, on33.indicZeroWidthMarksArgs(), "tmux 3.3 on the socket has no codepoint-widths")

	assert.Equal(t, "3.6a", socketTmuxVersionString("new36"))
	assert.True(t, indicZeroWidthMarksInfo(socketTmuxVersionString("new36")).Applied)
}

// The tmux client agent-deck runs on a socket with its own binary carries
// that binary's directory first on PATH: tmux hands an unattached client's
// PATH to the pane it starts (new-session on start, respawn-pane on
// restart), so a bare `tmux` inside the pane matches its server. Other
// sockets keep PATH as it is.
func TestSocketBinaryClient_PutsItsDirFirstOnPATH(t *testing.T) {
	bin := fakeTmuxBinary(t, "3.6a")
	setSocketBinariesForTest(t, map[string]string{"deck36": bin})
	t.Setenv("PATH", "/usr/bin:/bin")
	want := "PATH=" + filepath.Dir(bin) + ":/usr/bin:/bin"

	assert.Contains(t, tmuxExec("deck36", "respawn-pane", "-k", "-t", "x").Env, want, "respawn-pane (restart)")
	assert.Contains(t, tmuxExecContext(t.Context(), "deck36", "list-sessions").Env, want)
	start := newSessionSpawnCommand("deck36", bin, "new-session", "-d")
	assert.Contains(t, start.Env, want, "new-session (start)")
	assert.Equal(t, SpawnBaseDir, start.Dir)

	for _, kv := range tmuxExec("other", "list-sessions").Env {
		if strings.HasPrefix(kv, "PATH=") {
			assert.Equal(t, "PATH=/usr/bin:/bin", kv, "a socket without a binary keeps PATH")
		}
	}
	assert.Contains(t, withSocketBinaryPATH("deck36", []string{"HOME=/h"}), "PATH="+filepath.Dir(bin))

	// nil env means "inherit": the client keeps the rest of the environment.
	// A client left with only PATH would put its server under the default
	// socket directory instead of $TMUX_TMPDIR.
	t.Setenv("TMUX_TMPDIR", "/nonexistent/tmuxdir")
	t.Setenv("CLAUDE_JOB_DIR", "")
	os.Unsetenv("CLAUDE_JOB_DIR") //nolint:errcheck // restored by t.Setenv's cleanup
	inherited := withSocketBinaryPATH("deck36", nil)
	assert.Contains(t, inherited, "TMUX_TMPDIR=/nonexistent/tmuxdir")
	assert.Contains(t, inherited, want)
	assert.Contains(t, newSessionSpawnCommand("deck36", bin, "new-session").Env, "TMUX_TMPDIR=/nonexistent/tmuxdir")
}

// Start's stale-socket recovery runs for a direct spawn only. With a binary
// configured for the socket, the direct launcher is that path, not "tmux",
// and the recovery must still run.
func TestIsDirectTmuxLauncher_SocketBinary(t *testing.T) {
	const bin = "/opt/tmux-3.6/bin/tmux"
	setSocketBinariesForTest(t, map[string]string{"deck36": bin})

	assert.True(t, isDirectTmuxLauncher("", "tmux"))
	assert.True(t, isDirectTmuxLauncher("deck36", bin))
	assert.False(t, isDirectTmuxLauncher("deck36", "systemd-run"))
	assert.False(t, isDirectTmuxLauncher("", "systemd-run"))

	s := &Session{Name: "agentdeck_bin_1234abcd", WorkDir: "/tmp/project", LaunchAs: "direct", SocketName: "deck36"}
	launcher, _ := s.startCommandSpec("/tmp/project", "")
	assert.True(t, isDirectTmuxLauncher(s.SocketName, launcher), "direct launcher %q", launcher)
}
