package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Remote parity walk on g14 (2026-09-18): a session spawned by a remote
// agent-deck runs under the non-login SSH PATH, so a `claude` that lives only
// in ~/.local/bin is not found and the pane dies in ~260ms with a generic
// spawn_died_fast. These tests pin the PATH the spawn environment gets and the
// reason a still-unresolvable tool is reported with.

func fakeIsDir(existing ...string) func(string) bool {
	set := map[string]bool{}
	for _, d := range existing {
		set[d] = true
	}
	return func(dir string) bool { return set[dir] }
}

func TestSpawnPathCandidates_OrderAndPlatform(t *testing.T) {
	got := spawnPathCandidates("/home/u", "/opt/deck/agent-deck", "/srv/bin/agent-deck", "linux")
	assert.Equal(t, []string{"/home/u/.local/bin", "/home/u/bin", "/opt/deck", "/srv/bin"}, got)

	got = spawnPathCandidates("/Users/u", "", "", "darwin")
	assert.Equal(t, []string{"/Users/u/.local/bin", "/Users/u/bin", "/opt/homebrew/bin"}, got)

	// A bare name for the configured agent-deck path names no directory.
	got = spawnPathCandidates("/home/u", "", "agent-deck", "linux")
	assert.Equal(t, []string{"/home/u/.local/bin", "/home/u/bin"}, got)

	// No home: nothing home-relative is guessed.
	got = spawnPathCandidates("", "/opt/deck/agent-deck", "", "linux")
	assert.Equal(t, []string{"/opt/deck"}, got)
}

func TestMissingPathDirs_PresentAbsentDuplicatesOrder(t *testing.T) {
	isDir := fakeIsDir("/home/u/.local/bin", "/home/u/bin", "/opt/deck")
	candidates := []string{"/home/u/.local/bin", "/home/u/bin", "/opt/deck", "/home/u/.local/bin", "/nope/bin"}

	// All absent from PATH: kept in candidate order, deduplicated, and only
	// the dirs that exist.
	got := missingPathDirs("/usr/bin:/bin", candidates, isDir)
	assert.Equal(t, []string{"/home/u/.local/bin", "/home/u/bin", "/opt/deck"}, got)

	// Already on PATH (anywhere, including a trailing slash spelling): not
	// added again.
	got = missingPathDirs("/usr/bin:/home/u/bin/:/opt/deck:/bin", candidates, isDir)
	assert.Equal(t, []string{"/home/u/.local/bin"}, got)

	// Nothing missing: nothing to do.
	got = missingPathDirs("/home/u/.local/bin:/home/u/bin:/opt/deck", candidates, isDir)
	assert.Empty(t, got)

	// Empty PATH still gets the existing dirs.
	got = missingPathDirs("", candidates, isDir)
	assert.Equal(t, []string{"/home/u/.local/bin", "/home/u/bin", "/opt/deck"}, got)
}

func TestPrependPathDirs_KeepsUserOrder(t *testing.T) {
	assert.Equal(t, "/a:/b:/usr/bin:/bin", prependPathDirs("/usr/bin:/bin", []string{"/a", "/b"}))
	assert.Equal(t, "/usr/bin:/bin", prependPathDirs("/usr/bin:/bin", nil))
	assert.Equal(t, "/a", prependPathDirs("", []string{"/a"}))
}

// The shell snippet is what the pane actually evaluates, so run it under
// bash and read PATH back: dirs are prepended in order, a dir already on
// the pane's PATH is not added a second time, and what the user had is left
// exactly where it was.
func TestBuildSpawnPathExport_UnderBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	missing := filepath.Join(root, "missing")
	require.NoError(t, os.MkdirAll(a, 0o755))
	require.NoError(t, os.MkdirAll(b, 0o755))

	snippet := buildSpawnPathExport([]string{a, missing, b})
	require.NotEmpty(t, snippet)
	assert.NotContains(t, snippet, "exec ", "must not look like an exec launcher to wrapExitToShell")

	run := func(path string) string {
		cmd := exec.Command("bash", "-c", snippet+`printf '%s' "$PATH"`)
		cmd.Env = []string{"PATH=" + path, "HOME=" + root}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "snippet failed: %s", out)
		return string(out)
	}

	// Both absent: a then b, then the pane's PATH untouched.
	assert.Equal(t, a+":"+b+":/usr/bin:/bin", run("/usr/bin:/bin"))
	// b already there (in the middle): only a is added; b is not moved.
	assert.Equal(t, a+":/usr/bin:"+b+":/bin", run("/usr/bin:"+b+":/bin"))
	// Both present: PATH byte-identical.
	assert.Equal(t, "/usr/bin:"+a+":"+b, run("/usr/bin:"+a+":"+b))
	// The snippet is idempotent when evaluated twice.
	cmd := exec.Command("bash", "-c", snippet+snippet+`printf '%s' "$PATH"`)
	cmd.Env = []string{"PATH=/usr/bin", "HOME=" + root}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, a+":"+b+":/usr/bin", string(out))

	assert.Empty(t, buildSpawnPathExport(nil))
}

func TestSpawnToolBinaryFromCommand(t *testing.T) {
	cases := map[string]string{
		`export AGENTDECK_INSTANCE_ID=abc; export AGENTDECK_PROFILE='_test'; exec env -u TELEGRAM_STATE_DIR -u TELEGRAM_BOT_TOKEN claude --session-id 1 --name 'x y'`: "claude",
		`export A=b; exec cdw --resume 123`: "cdw",
		`npx codex@0.144`:                   "npx",
		`/opt/tools/gemini --yolo`:          "/opt/tools/gemini",
		`export COLORFGBG='15;0' && unset TELEGRAM_STATE_DIR TELEGRAM_BOT_TOKEN && export A=b; exec env -u TELEGRAM_STATE_DIR claude --session-id "1"`: "claude",
		`export X=1 && [ -f ~/.env ] && . ~/.env && cmd --flag=1`:                                                                                      "cmd",
		`cmd | tee log`:         "cmd",
		`$(which tool) --x`:     "",
		`tool > out`:            "tool",
		`bash -c 'exec claude'`: "bash",
		`$HOME/bin/tool`:        "",
		``:                      "",
		`export A=b;`:           "",
	}
	for cmd, want := range cases {
		assert.Equal(t, want, spawnToolBinaryFromCommand(cmd), "command %q", cmd)
	}
}

func TestSpawnPathCoversUserBinDir(t *testing.T) {
	// Known home: exact match under it, nothing else.
	for dir, want := range map[string]bool{
		"/tmp/x/home/.local/bin": true,
		"/tmp/x/home/bin":        true,
		"/tmp/x/home/opt/bin":    false,
		"/home/ashesh/bin":       false,
	} {
		assert.Equal(t, want, spawnPathCoversUserBinDir(dir, "/tmp/x/home"), dir)
	}
	// Unknown home: judged by shape.
	for dir, want := range map[string]bool{
		"/home/ashesh/.local/bin": true,
		"/home/ashesh/bin":        true,
		"/Users/a/.local/bin":     true,
		"/root/bin":               true,
		"/usr/local/bin":          false,
		"/opt/agent-deck/bin":     false,
		"/home/a/b/bin":           false,
	} {
		assert.Equal(t, want, spawnPathCoversUserBinDir(dir, ""), dir)
	}
}

func TestSpawnToolNotFoundReason(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "present"), []byte("#!/bin/sh\n"), 0o755))

	searched := "/usr/bin:" + bin
	assert.Equal(t, "", spawnToolNotFoundReason("present", searched), "a resolvable tool is not 'not found'")
	assert.Equal(t, "", spawnToolNotFoundReason("", searched), "no binary known: fall back to the generic reason")
	assert.Equal(t, "tool not found on PATH: claude (searched: "+searched+")", spawnToolNotFoundReason("claude", searched))
	// An absolute path is checked as a file, never resolved on PATH.
	assert.Equal(t, "", spawnToolNotFoundReason(filepath.Join(bin, "present"), "/usr/bin"))
	assert.Equal(t, "tool not found on PATH: /nope/claude (searched: /usr/bin)", spawnToolNotFoundReason("/nope/claude", "/usr/bin"))
}

// A spawn that dies fast because its tool is not on PATH must be recorded
// with the explicit reason (not the generic spawn_died_fast), and every
// surface that renders a record must say so: the preview block, the
// SpawnFailedError the CLI prints, and the JSON reason.
func TestSpawnFailureRecord_ToolNotFoundSurfaces(t *testing.T) {
	rec := &SpawnFailureRecord{
		InstanceID: "x", Tool: "claude",
		Command:   "export A=b; exec claude --session-id 1",
		Reason:    "tool not found on PATH: claude (searched: /usr/bin:/bin)",
		ElapsedMs: 261,
	}
	assert.True(t, rec.IsToolNotFound())
	disp := rec.FormatForDisplay()
	assert.Contains(t, disp, "session failed to start")
	assert.Contains(t, disp, "claude")
	assert.Contains(t, disp, "not found on PATH: claude")
	assert.Contains(t, disp, "searched: /usr/bin:/bin")
	assert.NotContains(t, disp, "exited almost immediately", "the not-found explanation replaces the generic one")

	err := &SpawnFailedError{TmuxName: "agentdeck_x", Record: rec}
	assert.Contains(t, err.Error(), "tool not found on PATH: claude (searched: /usr/bin:/bin)")
	assert.Contains(t, err.Error(), "261ms")

	generic := &SpawnFailureRecord{Reason: "spawn_died_fast", ElapsedMs: 5}
	assert.False(t, generic.IsToolNotFound())
	assert.Contains(t, generic.FormatForDisplay(), "exited almost immediately")
}

// prepareCommand is the single point every spawn path goes through, so the
// PATH export lands inside every wrapper (bash -c, launch shell) and never
// reorders what the pane already has. With nothing missing the command is
// untouched, which is what keeps the exact-shape tests above this one honest.
func TestPrepareCommand_PrependsMissingUserBinDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	localBin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(localBin, 0o755))
	// /opt/homebrew/bin is a candidate on macOS; keep it on PATH so the only
	// missing dir is the one this test plants.
	t.Setenv("PATH", "/usr/bin:/bin:/opt/homebrew/bin")
	// A host session's account would add its own export prefix and bash -c
	// wrap; this test pins the PATH prelude, not that.
	t.Setenv("AGENTDECK_ACCOUNT", "")
	ClearUserConfigCache()

	inst := NewInstance("spawn-path-prepare", "/tmp")
	inst.Tool = "claude"
	got, _, err := inst.prepareCommand("exec claude --session-id 1")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, buildSpawnPathExport([]string{localBin})), "got %q", got)
	assert.True(t, strings.HasSuffix(got, "exec claude --session-id 1"), "got %q", got)
	assert.Equal(t, 1, strings.Count(got, localBin), "each dir is named exactly once")

	// Already on PATH: byte-identical command.
	t.Setenv("PATH", "/usr/bin:"+localBin+":/bin:/opt/homebrew/bin")
	got, _, err = inst.prepareCommand("exec claude --session-id 1")
	require.NoError(t, err)
	assert.Equal(t, "exec claude --session-id 1", got)

	// A wrapper keeps the export inside the bash -c payload.
	t.Setenv("PATH", "/usr/bin:/bin:/opt/homebrew/bin")
	inst.Wrapper = "{command} --extra"
	got, _, err = inst.prepareCommand("tool")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "bash -c '"), "got %q", got)
	assert.Contains(t, got, localBin)
	assert.True(t, strings.HasSuffix(got, "tool --extra'"), "got %q", got)

	// An empty command (interactive shell pane) gets nothing prepended.
	inst.Wrapper = ""
	inst.Tool = "shell"
	got, _, err = inst.prepareCommand("")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// An --ssh session's command runs on the remote under the same non-login
// PATH, but its directories cannot be checked from here: the home dirs go
// over as $HOME expressions for the remote shell, the configured
// agent_deck_path's directory as a literal, and the prelude's own -d test
// decides. Nothing local (the deck binary's dir, /opt/homebrew/bin) leaks.
func TestPrepareCommand_SSHSessionGetsRemotePathPrelude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTDECK_ACCOUNT", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "agent-deck"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte(`
[remotes.lab]
host = "alice@lab"
agent_deck_path = "/home/alice/.local/bin/agent-deck"
`), 0o644))
	ClearUserConfigCache()

	inst := NewInstance("spawn-path-ssh", "/tmp")
	inst.Tool = "claude"
	inst.SSHHost = "alice@lab"
	inst.SSHRemotePath = "/srv/app"
	got, _, err := inst.prepareCommand("exec claude --session-id 1")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "ssh -t "), "got %q", got)
	assert.Contains(t, got, `for __d in "$HOME/.local/bin" "$HOME/bin" /home/alice/.local/bin;`)
	assert.NotContains(t, got, "/opt/homebrew/bin")
	assert.NotContains(t, got, home)
	assert.True(t, strings.HasSuffix(got, "exec claude --session-id 1'"), "got %q", got)

	// The prelude precedes the export prefix inside the remote program, after
	// the cd, so the remote shell evaluates it before the tool execs.
	assert.Less(t, strings.Index(got, "cd /srv/app"), strings.Index(got, "for __d in"))
}

// The Docker integration test: a fake `claude` that lives ONLY in
// ~/.local/bin, with ~/.local/bin absent from the process PATH (the remote
// agent's non-login SSH environment). Pre-fix the pane died in ~250ms with
// spawn_died_fast; now the spawn finds it. The negative half: with no claude
// anywhere the record names the tool and the searched PATH.
func TestSpawnPath_FakeClaudeOnlyInLocalBin(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ClearUserConfigCache()

	// PATH keeps only what tmux/bash/sh need: ~/.local/bin is deliberately
	// not on it, and neither is any host directory that happens to hold a
	// real claude.
	var keep []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || strings.HasPrefix(dir, home) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "claude")); err == nil {
			continue
		}
		keep = append(keep, dir)
	}
	t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
	t.Setenv("AGENTDECK_ACCOUNT", "")

	localBin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(localBin, 0o755))
	marker := filepath.Join(home, "claude-ran")
	fake := "#!/bin/sh\nprintf 'fake-claude %s\\n' \"$*\" > " + marker + "\nsleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(localBin, "claude"), []byte(fake), 0o755))
	_, lookErr := exec.LookPath("claude")
	require.Error(t, lookErr, "precondition: claude must not resolve on the process PATH")

	inst := NewInstanceWithTool("spawn-path-localbin", t.TempDir(), "claude")
	t.Cleanup(func() { _ = inst.Kill(); clearSpawnFailureRecord(inst.ID) })
	require.NoError(t, inst.Start())
	require.NoError(t, inst.VerifySpawned(3*time.Second), "the fake claude in ~/.local/bin must be found")
	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}, 5*time.Second, 100*time.Millisecond, "the fake claude never ran")
	assert.Nil(t, inst.SpawnFailure())

	// Negative: no claude anywhere → the reason names the tool and the PATH
	// that was searched (with ~/.local/bin in it).
	require.NoError(t, inst.Kill())
	require.NoError(t, os.Remove(filepath.Join(localBin, "claude")))
	missing := NewInstanceWithTool("spawn-path-nobin", t.TempDir(), "claude")
	t.Cleanup(func() { _ = missing.Kill(); clearSpawnFailureRecord(missing.ID) })
	require.NoError(t, missing.Start())
	err := missing.VerifySpawned(8 * time.Second)
	require.Error(t, err)
	var spawnErr *SpawnFailedError
	require.ErrorAs(t, err, &spawnErr)
	require.NotNil(t, spawnErr.Record)
	assert.True(t, strings.HasPrefix(spawnErr.Record.Reason, "tool not found on PATH: claude (searched: "), "reason = %q", spawnErr.Record.Reason)
	assert.Contains(t, spawnErr.Record.Reason, localBin)
	assert.Contains(t, err.Error(), "tool not found on PATH: claude")
}
