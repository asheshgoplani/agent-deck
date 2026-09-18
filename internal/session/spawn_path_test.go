package session

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Remote parity walk on g14 (2026-09-18): a session spawned by a remote
// agent-deck runs under the non-login SSH PATH, so a `claude` that lives only
// in ~/.local/bin is not found and the pane dies in ~260ms with a generic
// spawn_died_fast. These tests pin the PATH the spawn environment gets, which
// directories may ever be put in front of it, and the pane-side evidence a
// still-unresolvable tool is reported on.

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

	// A relative candidate names no directory the pane could be trusted to
	// resolve, whatever the predicate says.
	got = missingPathDirs("", []string{"bin", "./bin", "/home/u/bin"}, func(string) bool { return true })
	assert.Equal(t, []string{"/home/u/bin"}, got)
}

// statInfo is an os.FileInfo with a chosen owner, for the ownership arm of
// spawnPathDirInfoUsable (a test cannot chown to another user).
type statInfo struct {
	fs.FileInfo
	uid uint32
}

func (s statInfo) Sys() any { return &syscall.Stat_t{Uid: s.uid} }

// Only a directory nobody but its owner can write to, owned by this user or
// root, may be put in front of every session's PATH. Running the deck from
// /tmp (world-writable, sticky), a dir another user owns, or a file must
// never get that dir prepended.
func TestSpawnPathDirUsable_RejectsWorldWritableAndForeign(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, mode os.FileMode) string {
		dir := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, mode))
		return dir
	}
	private := mk("private", 0o755)
	groupWritable := mk("group", 0o775)
	worldWritable := mk("world", 0o777)
	sticky := mk("sticky", 0o1777)
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o755))

	assert.True(t, spawnPathDirUsable(private))
	assert.True(t, spawnPathDirUsable(groupWritable), "group-writable is the owner's call")
	assert.False(t, spawnPathDirUsable(worldWritable), "world-writable: anyone could plant a binary")
	assert.False(t, spawnPathDirUsable(sticky), "the sticky bit does not make /tmp-like dirs safe")
	assert.False(t, spawnPathDirUsable(file), "not a directory")
	assert.False(t, spawnPathDirUsable(filepath.Join(root, "missing")))

	// Ownership: this user or root, nobody else.
	info, err := os.Stat(private)
	require.NoError(t, err)
	me := uint32(os.Geteuid())
	assert.True(t, spawnPathDirInfoUsable(statInfo{info, me}, os.Geteuid()))
	assert.True(t, spawnPathDirInfoUsable(statInfo{info, 0}, os.Geteuid()), "root-owned system dirs are fine")
	assert.False(t, spawnPathDirInfoUsable(statInfo{info, me + 1}, os.Geteuid()), "another user's dir is theirs to change")

	// The real filter drops such a dir even when it is missing from PATH.
	got := missingPathDirs("/usr/bin", []string{worldWritable, private, sticky}, spawnPathDirUsable)
	assert.Equal(t, []string{private}, got)
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

	// The --ssh variant cannot be filtered here, so the remote shell asks
	// for ownership too: the user's own dir is added, a root-owned system
	// dir (/usr/bin exists everywhere and is never the test user's) is not.
	remote := buildSpawnPathExportWords([]string{shellescape.Quote(a), "/usr/bin"}, sshSpawnPathTest)
	cmd = exec.Command("bash", "-c", remote+`printf '%s' "$PATH"`)
	cmd.Env = []string{"PATH=/bin", "HOME=" + root}
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if os.Geteuid() == 0 {
		assert.Equal(t, a+":/usr/bin:/bin", string(out), "root owns /usr/bin")
	} else {
		assert.Equal(t, a+":/bin", string(out))
	}
}

// The probe target is the first program the pane runs, and only when
// nothing ahead of it can change how it resolves. Every refused shape here
// is one the round-1 review showed would have been misattributed: an env
// file or init script that may extend PATH, a PATH export, a builtin first
// word (`cd ~/proj && ./run.sh`), an expansion.
func TestSpawnToolProbeTarget(t *testing.T) {
	cases := map[string]string{
		`export AGENTDECK_INSTANCE_ID=abc; export AGENTDECK_PROFILE='_test'; exec env -u TELEGRAM_STATE_DIR -u TELEGRAM_BOT_TOKEN claude --session-id 1 --name 'x y'`: "claude",
		`export A=b; exec cdw --resume 123`: "cdw",
		`npx codex@0.144`:                   "npx",
		`/opt/tools/gemini --yolo`:          "/opt/tools/gemini",
		`export COLORFGBG='15;0' && unset TELEGRAM_STATE_DIR TELEGRAM_BOT_TOKEN && export A=b; exec env -u TELEGRAM_STATE_DIR claude --session-id "1"`: "claude",
		`cmd | tee log`:         "cmd",
		`tool > out`:            "tool",
		`bash -c 'exec claude'`: "bash",
		`[ -n "$X" ] && claude`: "claude",
		// Refused: something before the program may change the lookup.
		`export X=1 && [ -f ~/.env ] && . ~/.env && cmd --flag=1`: "",
		`source "/home/u/.nvm/nvm.sh" && claude`:                  "",
		`eval "$(direnv hook bash)" && claude`:                    "",
		`export PATH='/opt/x/bin' && exec claude`:                 "",
		`export A=b PATH=/x; exec claude`:                         "",
		`PATH=/x:$PATH claude`:                                    "",
		`unset PATH; claude`:                                      "",
		// Refused: the first word is not a program PATH answers for.
		`cd ~/proj && ./run.sh`:         "",
		`echo hi && tool`:               "",
		`if [ -x tool ]; then tool; fi`: "",
		// Refused: an expansion or nothing to run.
		`$(which tool) --x`: "",
		`$HOME/bin/tool`:    "",
		``:                  "",
		`export A=b;`:       "",
	}
	for cmd, want := range cases {
		assert.Equal(t, want, spawnToolProbeTarget(cmd), "command %q", cmd)
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

// The verdict comes from the marker the pane's probe leaves, and from
// nothing else: no marker, no attribution, whatever this process's PATH
// says. The marker is consumed once read.
func TestSpawnToolNotFoundReason_MarkerOnly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "x.notfound")
	assert.Equal(t, "", spawnToolNotFoundReason("claude", marker), "no marker: no evidence")
	assert.Equal(t, "", spawnToolNotFoundReason("", marker), "no probe target: nothing to attribute")

	require.NoError(t, os.WriteFile(marker, []byte("/home/u/.local/bin:/usr/bin:/bin\n"), 0o600))
	assert.Equal(t, "tool not found on PATH: claude (searched: /home/u/.local/bin:/usr/bin:/bin)", spawnToolNotFoundReason("claude", marker))
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "the marker is consumed with the verdict")

	require.NoError(t, os.WriteFile(marker, []byte("\n"), 0o600))
	assert.Equal(t, "tool not found on PATH: claude (searched: (empty PATH))", spawnToolNotFoundReason("claude", marker))
	assert.Equal(t, "", spawnToolNotFoundReason("claude", ""), "no marker path: no probe was emitted")
}

// The probe is what the pane actually evaluates, so run prelude + probe under
// bash and look at the marker: written with the pane's PATH (the prepended
// dir included) only when the pane cannot resolve the tool; a tool found in
// a prepended dir, or a shell function of that name (what a launch shell's
// rc file defines), leaves no marker.
func TestBuildSpawnToolProbe_UnderBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "local bin") // a space: the marker and dirs must survive quoting
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "present"), []byte("#!/bin/sh\n"), 0o755))
	marker := filepath.Join(root, "spawn failure", "id.notfound")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o700))

	run := func(tool, preamble string) (string, bool) {
		t.Helper()
		_ = os.Remove(marker)
		snippet := preamble + buildSpawnPathExport([]string{bin}) + buildSpawnToolProbe(tool, marker)
		cmd := exec.Command("bash", "-c", snippet+`printf '%s' "$PATH"`)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "snippet failed: %s", out)
		data, err := os.ReadFile(marker)
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(data)), true
	}

	searched, written := run("absent-tool", "")
	require.True(t, written, "a tool the pane cannot resolve leaves the marker")
	assert.Equal(t, bin+":/usr/bin:/bin", searched, "the marker holds the pane's PATH, prepended dir first")

	_, written = run("present", "")
	assert.False(t, written, "a tool found through the prepended dir leaves no marker")

	_, written = run("absent-tool", "absent-tool() { :; }; ")
	assert.False(t, written, "a function of that name (launch shell rc) resolves; no marker")

	_, written = run("cd", "")
	assert.False(t, written, "a builtin resolves; no marker even if probed")

	assert.NotContains(t, buildSpawnToolProbe("claude", marker), "exec ", "must not look like an exec launcher to wrapExitToShell")
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
	marker := spawnToolProbeMarkerPath(inst.ID)
	assert.Equal(t, buildSpawnPathExport([]string{localBin})+buildSpawnToolProbe("claude", marker)+"exec claude --session-id 1", got)
	assert.Equal(t, 1, strings.Count(got, localBin), "each dir is named exactly once")
	probeTool, probeMarker := inst.spawnToolLookup("exec claude --session-id 1")
	assert.Equal(t, "claude", probeTool, "the watcher is told the same tool the pane probes")
	assert.Equal(t, marker, probeMarker)
	// A stale marker from an earlier spawn is dropped when the probe is
	// emitted, so it can never be read as this spawn's evidence.
	require.NoError(t, os.WriteFile(marker, []byte("/stale\n"), 0o600))
	_, _, err = inst.prepareCommand("exec claude --session-id 1")
	require.NoError(t, err)
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "stale marker must be removed at spawn")

	// A shape the probe cannot judge (an env file may extend PATH) gets the
	// prelude alone, and the watcher is told there is no probe.
	got, _, err = inst.prepareCommand("[ -f ~/.env ] && . ~/.env && exec claude --session-id 1")
	require.NoError(t, err)
	assert.Equal(t, buildSpawnPathExport([]string{localBin})+"[ -f ~/.env ] && . ~/.env && exec claude --session-id 1", got)
	probeTool, probeMarker = inst.spawnToolLookup("[ -f ~/.env ] && . ~/.env && exec claude --session-id 1")
	assert.Equal(t, "", probeTool)
	assert.Equal(t, "", probeMarker)

	// Already on PATH: byte-identical command, no prelude and no probe.
	t.Setenv("PATH", "/usr/bin:"+localBin+":/bin:/opt/homebrew/bin")
	got, _, err = inst.prepareCommand("exec claude --session-id 1")
	require.NoError(t, err)
	assert.Equal(t, "exec claude --session-id 1", got)
	probeTool, probeMarker = inst.spawnToolLookup("exec claude --session-id 1")
	assert.Equal(t, "", probeTool, "no prelude, no probe: the generic reason stands")
	assert.Equal(t, "", probeMarker)

	// A wrapper keeps the prelude and probe inside the bash -c payload.
	t.Setenv("PATH", "/usr/bin:/bin:/opt/homebrew/bin")
	inst.Wrapper = "{command} --extra"
	got, _, err = inst.prepareCommand("tool")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "bash -c '"), "got %q", got)
	assert.Contains(t, got, localBin)
	assert.Contains(t, got, "command -v tool ")
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
	assert.Contains(t, got, `[ -d "$__d" ] && [ -O "$__d" ]`, "the remote shell checks ownership, this process cannot")
	assert.NotContains(t, got, "/opt/homebrew/bin")
	assert.NotContains(t, got, home)
	assert.NotContains(t, got, "command -v", "no probe: the marker would be on the other host")
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
	// VerifySpawned answers "alive" the instant the pane still exists, so
	// wait for the watcher's record (the pane dies within a few hundred ms)
	// before reading the verdict.
	recordFor := func(inst *Instance) *SpawnFailureRecord {
		t.Helper()
		var rec *SpawnFailureRecord
		require.Eventually(t, func() bool {
			rec = inst.SpawnFailure()
			return rec != nil
		}, 15*time.Second, 100*time.Millisecond, "the pane must die and be recorded")
		return rec
	}
	rec := recordFor(missing)
	assert.True(t, strings.HasPrefix(rec.Reason, "tool not found on PATH: claude (searched: "+localBin+":"), "the searched PATH is the pane's, prepended dir first: %q", rec.Reason)
	err := missing.VerifySpawned(2 * time.Second)
	require.Error(t, err)
	var spawnErr *SpawnFailedError
	require.ErrorAs(t, err, &spawnErr)
	assert.Contains(t, err.Error(), "tool not found on PATH: claude")
	_, statErr := os.Stat(spawnToolProbeMarkerPath(missing.ID))
	assert.True(t, os.IsNotExist(statErr), "the marker is consumed with the verdict")

	// Misattribution guard (round-1 review, finding 1): the pane resolves
	// `claude` through an env file that extends PATH, which this process
	// never sees, and the tool then dies for its own reason. The deck's own
	// PATH lookup would have blamed PATH; the pane's evidence says nothing
	// of the kind, so the generic reason stands with the dying output.
	envBin := filepath.Join(home, "env-bin")
	require.NoError(t, os.MkdirAll(envBin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envBin, "claude"), []byte("#!/bin/sh\necho 'claude: config is broken' >&2\nexit 3\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env-path"), []byte("export PATH=\"$PATH:"+envBin+"\"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "agent-deck"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte("[shell]\nenv_files = [\"~/.env-path\"]\n"), 0o644))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	viaEnv := NewInstanceWithTool("spawn-path-envfile", t.TempDir(), "claude")
	t.Cleanup(func() { _ = viaEnv.Kill(); clearSpawnFailureRecord(viaEnv.ID) })
	require.NoError(t, viaEnv.Start())
	rec = recordFor(viaEnv)
	assert.Equal(t, "spawn_died_fast", rec.Reason, "no pane evidence of a missing tool: generic reason (dying output %q)", rec.DyingOutput)
	err = viaEnv.VerifySpawned(2 * time.Second)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not found on PATH")
}
