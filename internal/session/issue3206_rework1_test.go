package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// #3215 review, rework round 1.

// withDeadline runs f and fails (instead of hanging the package) when it
// does not return within d.
func withDeadline(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s hung for %s", what, d)
	}
}

// waitCodexDetectionDone registers a cleanup, run after every later one
// (cleanups are LIFO), that waits on codexDetections until no codex
// detection goroutine started by a Start/Restart is left. They run for ~4s
// after a spawn, and a later test file that swaps the package logger
// (issue662_session_data_diag_test.go) raced one under -race on CI; the
// WaitGroup gives the race detector the ordering.
func waitCodexDetectionDone(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() {
			codexDetections.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Errorf("codex session detection still running 20s after the test")
		}
	})
}

// R-1: a FIFO (or any non-regular file) must never be opened for reading: an
// O_RDONLY open of a FIFO blocks until a writer appears, which hung every
// spawn and `session show`/`session primer`.
func TestIssue3206Rework_SpecialFilesDoNotHang(t *testing.T) {
	identityTestEnv(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.md")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	link := filepath.Join(dir, "link-to-fifo.md")
	require.NoError(t, os.Symlink(fifo, link))
	cases := map[string]string{"fifo.md": fifo, "symlink to a fifo": link}
	if _, err := os.Stat("/dev/null"); err == nil {
		cases["/dev/null"] = "/dev/null"
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			inst := identityTestInstance("claude")
			inst.InstructionsFiles = []string{path}
			var files []InstructionsFileStatus
			withDeadline(t, 5*time.Second, "InstructionsFilesReport", func() { _, files = inst.InstructionsFilesReport() })
			require.Len(t, files, 1)
			require.Equal(t, InstructionsStateUnreadable, files[0].State)
			require.Equal(t, "not a regular file", files[0].Reason)
			withDeadline(t, 5*time.Second, "claude spawn command", func() { _ = inst.buildClaudeCommand("claude") })
			cx := identityTestInstance("codex")
			cx.InstructionsFiles = []string{path}
			withDeadline(t, 5*time.Second, "codex spawn command", func() { _ = cx.buildCodexCommand("codex") })
		})
	}
}

// R-2: a configured developer_instructions counts against codex's inline
// budget. With the operator's ~9 KB value there is no room, so the override
// stays byte-identical to the session without instruction files (it started
// fine before the flag) and the file is reported as not delivered.
func TestIssue3206Rework_LocalCodexBudgetCountsConfiguredDeveloperInstructions(t *testing.T) {
	t.Run("no room: override unchanged, file reported", func(t *testing.T) {
		home := identityTestEnv(t)
		t.Setenv("CODEX_HOME", "")
		codexHome := filepath.Join(home, ".codex")
		writeCodexDeveloperInstructions(t, codexHome, identityGoldenConfiguredDeveloperInstructions())
		file := writeInstructions(t, t.TempDir(), "role.md", strings.Repeat("r", 4500)+"\n")

		plain := identityTestInstance("codex")
		want, ok := plain.codexIdentityOverride(codexHome, plain.ProjectPath)
		require.True(t, ok)

		inst := identityTestInstance("codex")
		inst.InstructionsFiles = []string{file}
		got, ok := inst.codexIdentityOverride(codexHome, inst.ProjectPath)
		require.True(t, ok)
		// Byte-identical to the same session without the flag, which
		// TestIdentityGolden_LocalCodexConfiguredDeveloperInstructions pins
		// to main.
		require.Equal(t, want, got, "a file that cannot fit must not change the override")

		_, files := inst.InstructionsFilesReport()
		require.Len(t, files, 1)
		require.Equal(t, InstructionsStateNotDelivered, files[0].State)
		require.Contains(t, files[0].Reason, "developer_instructions")
	})

	t.Run("some room: merged override stays inside the budget", func(t *testing.T) {
		home := identityTestEnv(t)
		t.Setenv("CODEX_HOME", "")
		codexHome := filepath.Join(home, ".codex")
		writeCodexDeveloperInstructions(t, codexHome, strings.Repeat("Operator rule: keep it green.\n", 100)) // ~3 KB
		small := writeInstructions(t, t.TempDir(), "small.md", "SMALL-ROLE\n")
		big := writeInstructions(t, t.TempDir(), "big.md", "BIG-ROLE\n"+strings.Repeat("b", 4500)+"\n")
		inst := identityTestInstance("codex")
		inst.InstructionsFiles = []string{small, big}
		got, ok := inst.codexIdentityOverride(codexHome, inst.ProjectPath)
		require.True(t, ok)
		require.LessOrEqual(t, len(got), identityInlineMaxBytes, "existing + block must fit the 8 KB budget")
		value := tomlRoundTrip(t, got)
		require.Contains(t, value, "Operator rule: keep it green.")
		require.Contains(t, value, "SMALL-ROLE")
		require.NotContains(t, value, "BIG-ROLE")
		require.Contains(t, value, "\n### "+big+"\n(not included: ")
		_, files := inst.InstructionsFilesReport()
		require.Equal(t, InstructionsStateOK, files[0].State)
		require.Equal(t, InstructionsStateNotDelivered, files[1].State)
	})
}

// R-2, end to end: the session that started before the flag still starts
// with it (on 2f05d09f this failed with tmux "command too long").
func TestIssue3206Rework_LocalCodexRealSpawnWithConfiguredDeveloperInstructions(t *testing.T) {
	testutil.RequireTmux(t)
	home := isolatedHomeDir(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	binDir := filepath.Join(home, "bin")
	recDir := filepath.Join(home, "record")
	stub := issue3206FakeHarness(t, binDir, recDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfgPath, err := GetUserConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte(fmt.Sprintf("[codex]\ncommand = %q\n", stub)), 0o644))
	ClearUserConfigCache()
	writeCodexDeveloperInstructions(t, filepath.Join(home, ".codex"), identityGoldenConfiguredDeveloperInstructions())

	projectPath := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(projectPath, 0o755))
	file := filepath.Join(t.TempDir(), "role.md")
	require.NoError(t, os.WriteFile(file, []byte("ROLE "+strings.Repeat("r", 4500)+"\n"), 0o644))

	inst := NewInstanceWithTool("issue3206-rework-codex", projectPath, "codex")
	inst.InstructionsFiles = []string{file}
	waitCodexDetectionDone(t)
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Start(), "a codex session with a configured developer_instructions must still start with an instruction file")
	s := issue3206WaitSpawn(t, recDir, 1, 15*time.Second)
	require.Contains(t, s.identity, "Operator rule", "the operator's developer_instructions still lead")
	require.Contains(t, s.identity, "You are running inside agent-deck")
}

// R-3: an SSH codex launch carries the block twice (remote file + -c
// override), so a few small files may not fit even as per-file pointers.
// They must never vanish silently: the section stays with one summary
// pointer, the remote launch still works, and each file has a state.
func TestIssue3206Rework_SSHCodexSmallFilesNeverDroppedSilently(t *testing.T) {
	bin := remoteTestEnv(t)
	record := remoteHarnessRecorder(t, bin, "codex")
	dir := t.TempDir()
	var files []string
	for k := 0; k < 5; k++ {
		files = append(files, writeInstructions(t, dir, fmt.Sprintf("role-%d-%s.md", k, strings.Repeat("n", 40)), "ROLE "+strings.Repeat("s", 120)+"\n"))
	}
	inst := sshIdentityInstance("codex", "remote worker")
	inst.InstructionsFiles = files

	text, ok := inst.identityRemoteText()
	require.True(t, ok)
	require.Contains(t, text, "\n## Session instructions\n", "the section must not disappear")
	_, statuses := inst.InstructionsFilesReport()
	require.Len(t, statuses, 5)
	delivered := 0
	for _, st := range statuses {
		require.NotEmpty(t, st.State)
		if st.State == InstructionsStateOK {
			delivered++
			continue
		}
		require.Equal(t, InstructionsStateNotDelivered, st.State)
		require.Contains(t, st.Reason, "inline budget")
		require.Contains(t, text, st.Path, "an undelivered file is named in the block")
	}

	launch, _, err := inst.prepareCommand(inst.buildCodexCommand("codex"))
	require.NoError(t, err)
	r := runLaunch(t, launch, record, t.TempDir())
	override, ok := r.flagValue("-c")
	require.True(t, ok, "codex keeps its -c override: %q", r.args)
	value := tomlRoundTrip(t, override)
	require.Contains(t, value, "## Session instructions")
	for _, st := range statuses {
		if st.State != InstructionsStateOK {
			require.Contains(t, value, st.Path)
		}
	}
}

// CodeRabbit on #3215 (security, minor): the identity files copy the text
// of the instruction files, so with instruction files they are owner-only
// (0600) and never more readable than a private source. Without instruction
// files they stay 0644, as before.
func TestIssue3206Rework_IdentityFilesPrivateWithInstructions(t *testing.T) {
	identityTestEnv(t)
	secret := writeInstructions(t, t.TempDir(), "private.md", "PRIVATE-ROLE\n")
	require.NoError(t, os.Chmod(secret, 0o600))
	for _, tc := range []struct {
		files []string
		want  os.FileMode
	}{{nil, 0o644}, {[]string{secret}, 0o600}} {
		inst := identityTestInstance("copilot")
		inst.InstructionsFiles = tc.files
		dir, _, ok := inst.ensureIdentityFile()
		require.True(t, ok)
		for _, name := range []string{identityFileName, geminiIdentityFileName, filepath.Join(copilotIdentitySubdir, copilotIdentityFileName)} {
			st, err := os.Stat(filepath.Join(dir, name))
			require.NoError(t, err)
			require.Equal(t, tc.want, st.Mode().Perm(), "%s with %d instruction file(s)", name, len(tc.files))
		}
	}
}

// CodeRabbit on #3215 (minor): a later file that is shorter than its pointer
// line must be reserved at its own size, so two files that fit together in
// full are both delivered.
func TestIssue3206Rework_ShortLaterFileDoesNotPushOutAnEarlierOne(t *testing.T) {
	identityTestEnv(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "big.md")
	short := writeInstructions(t, dir, "short.md", "x\n")
	inst := identityTestInstance("codex")
	inst.InstructionsFiles = []string{big, short}
	level, _ := inst.EffectiveContextLevel()
	// Largest big file for which both files fit in full.
	n := 0
	for size := 8000; size > 0; size-- {
		require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("b", size)+"\n"), 0o600))
		full, _ := inst.contextBlock(level, budgetFile, "")
		if budgetCodexLocal.fits(full, "") {
			n = size
			break
		}
	}
	require.NotZero(t, n)
	full, _ := inst.contextBlock(level, budgetFile, "")
	got, files := inst.contextBlock(level, budgetCodexLocal, "")
	require.Equal(t, InstructionsStateOK, files[0].State, "big file (%d B) fits with the short one", n)
	require.Equal(t, InstructionsStateOK, files[1].State)
	require.Equal(t, full, got)
}
