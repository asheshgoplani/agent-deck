package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Issue #3206: per-session instruction files ride the identity block.

func writeInstructions(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestInstructionsFiles_NormalizePaths(t *testing.T) {
	home := identityTestEnv(t)
	t.Setenv("TEAM_DIR", "/srv/team")

	got, err := NormalizeInstructionsFiles([]string{"~/roles/review.md, $TEAM_DIR/rules.md", "", "/srv/team/rules.md", "/a/../b/c.md"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(home, "roles/review.md"), "/srv/team/rules.md", "/b/c.md"}, got,
		"~ and $VAR expanded, comma splits, duplicates dropped, paths cleaned")

	cleared, err := NormalizeInstructionsFiles([]string{""})
	require.NoError(t, err)
	require.Empty(t, cleared, "an empty value clears the list")

	for _, bad := range []string{"roles/review.md", "./x.md", "$UNSET_VAR_3206/x.md", "/tmp/a\nb.md", "/tmp/a\x1b[31m.md"} {
		_, err := NormalizeInstructionsFiles([]string{bad})
		require.Error(t, err, "%q must be refused", bad)
	}

	many := make([]string, maxInstructionsFiles+1)
	for k := range many {
		many[k] = filepath.Join("/x", strings.Repeat("f", k+1))
	}
	_, err = NormalizeInstructionsFiles(many)
	require.Error(t, err, "list longer than the cap must be refused")
}

func TestInstructionsFiles_SetFieldReplacesAndClears(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	_, _, err := SetField(inst, FieldInstructionsFile, "/x/a.md,/x/b.md", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"/x/a.md", "/x/b.md"}, inst.InstructionsFiles)
	require.Equal(t, FieldRestartRequired, RestartPolicyFor(FieldInstructionsFile))

	old, _, err := SetField(inst, FieldInstructionsFile, "/x/c.md", nil)
	require.NoError(t, err)
	require.Equal(t, "/x/a.md,/x/b.md", old)
	require.Equal(t, []string{"/x/c.md"}, inst.InstructionsFiles, "set replaces the list")

	_, _, err = SetField(inst, FieldInstructionsFile, "relative.md", nil)
	require.Error(t, err)
	require.Equal(t, []string{"/x/c.md"}, inst.InstructionsFiles, "a refused value leaves the list alone")

	_, _, err = SetField(inst, FieldInstructionsFile, "", nil)
	require.NoError(t, err)
	require.Empty(t, inst.InstructionsFiles, "empty clears")
}

func TestInstructionsFiles_SectionAndSafetyRules(t *testing.T) {
	identityTestEnv(t)
	dir := t.TempDir()
	role := writeInstructions(t, dir, "role.md", "\uFEFFYou review only.\r\n\tIndented line.\x1b[31m red\x07\u0085 end\n\n\n")
	binary := writeInstructions(t, dir, "bin.md", "abc\x00def")
	latin1 := writeInstructions(t, dir, "latin1.md", "caf\xe9")
	big := writeInstructions(t, dir, "big.md", strings.Repeat("x", instructionsFileMaxBytes+1))
	missing := filepath.Join(dir, "missing.md")
	subdir := filepath.Join(dir, "adir")
	require.NoError(t, os.Mkdir(subdir, 0o755))

	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{role, binary, latin1, big, missing, subdir}
	text, files := inst.InstructionsFilesReport()

	base := inst.BuildIdentityPrompt()
	require.True(t, strings.HasPrefix(text, base), "the section is appended after the unchanged identity block")
	section := strings.TrimPrefix(text, base)
	require.Contains(t, section, "\n## Session instructions\n")
	require.Contains(t, section, "\n### "+role+"\nYou review only.\n\tIndented line.[31m red end\n",
		"BOM, CR, ESC, BEL and C1 dropped; newline and tab kept; trailing newlines collapsed")
	for _, c := range []string{"\x1b", "\x07", "\r", "\x00", "\u0085", "\uFEFF"} {
		require.NotContains(t, section, c)
	}
	require.NotContains(t, section, strings.Repeat("x", 100), "an over-cap file is never inlined")

	want := []InstructionsFileStatus{
		{Path: role, State: InstructionsStateOK},
		{Path: binary, State: InstructionsStateUnreadable, Reason: "not UTF-8 text"},
		{Path: latin1, State: InstructionsStateUnreadable, Reason: "not UTF-8 text"},
		{Path: big, State: InstructionsStateNotDelivered, Reason: "larger than the 32 KB per-file cap"},
		{Path: missing, State: InstructionsStateUnreadable, Reason: "file not found"},
		{Path: subdir, State: InstructionsStateUnreadable, Reason: "not a regular file"},
	}
	require.Len(t, files, len(want))
	for k, w := range want {
		require.Equal(t, w.Path, files[k].Path)
		require.Equal(t, w.State, files[k].State, files[k].Path)
		require.Equal(t, w.Reason, files[k].Reason, files[k].Path)
		if k > 0 {
			// Every file that is not in the block gets a pointer line naming why.
			require.Contains(t, section, "\n### "+w.Path+"\n(not included: "+w.Reason+"; read the file itself)\n")
		}
	}

	// The written identity file (what claude, pi, gemini, opencode, copilot and
	// env-only tools read) carries exactly that text, for every harness.
	for _, tool := range []string{"claude", "pi", "omp", "gemini", "opencode", "copilot", "hermes", "cursor", "crush", "shell"} {
		ti := identityTestInstance(tool)
		ti.InstructionsFiles = []string{role}
		_, file, ok := ti.ensureIdentityFile()
		require.True(t, ok, tool)
		got, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Contains(t, string(got), "You review only.", tool)
		if tool == "gemini" {
			g, err := os.ReadFile(filepath.Join(filepath.Dir(file), geminiIdentityFileName))
			require.NoError(t, err)
			require.Equal(t, string(got), string(g))
		}
		if tool == "copilot" {
			c, err := os.ReadFile(filepath.Join(filepath.Dir(file), copilotIdentitySubdir, copilotIdentityFileName))
			require.NoError(t, err)
			require.Equal(t, string(got), string(c))
		}
	}
}

func TestInstructionsFiles_PrimerLevelCarriesThem(t *testing.T) {
	identityTestEnv(t)
	role := writeInstructions(t, t.TempDir(), "role.md", "PRIMER-ROLE\n")
	inst := identityTestInstance("claude")
	inst.ContextLevel = ContextLevelPrimer
	inst.InstructionsFiles = []string{role}
	text, files := inst.InstructionsFilesReport()
	require.True(t, strings.HasPrefix(text, inst.BuildPrimerPrompt()))
	require.Contains(t, text, "PRIMER-ROLE")
	require.Equal(t, InstructionsStateOK, files[0].State)
}

func TestInstructionsFiles_OptOutsSuppressThem(t *testing.T) {
	identityTestEnv(t)
	role := writeInstructions(t, t.TempDir(), "role.md", "SUPPRESSED-ROLE\n")
	for name, set := range map[string]func(*Instance){
		"--no-identity":      func(i *Instance) { i.IdentityInjectionDisabled = true },
		"context-level none": func(i *Instance) { i.ContextLevel = ContextLevelNone },
	} {
		t.Run(name, func(t *testing.T) {
			for _, tool := range []string{"claude", "codex"} {
				inst := identityTestInstance(tool)
				inst.InstructionsFiles = []string{role}
				set(inst)
				text, files := inst.InstructionsFilesReport()
				require.Empty(t, text)
				require.Len(t, files, 1)
				require.Equal(t, InstructionsStateNotDelivered, files[0].State)
				require.Contains(t, files[0].Reason, "context level none")
				var cmd string
				if tool == "claude" {
					cmd = inst.buildClaudeCommand("claude")
				} else {
					cmd = inst.buildCodexCommand("codex")
				}
				require.NotContains(t, cmd, "SUPPRESSED-ROLE")
				require.NotContains(t, cmd, "append-system-prompt")
				require.NotContains(t, cmd, "developer_instructions")
				file, _ := inst.IdentityFilePath()
				_, err := os.Stat(file)
				require.True(t, os.IsNotExist(err), "no identity file is written")
			}
		})
	}
}

// Inline budget: local codex. The file keeps every instruction file; the -c
// override, which rides in the pane command, gets pointer lines for what
// does not fit, in order, so a later small file still goes in.
func TestInstructionsFiles_CodexLocalBudgetPointsInOrder(t *testing.T) {
	identityTestEnv(t)
	dir := t.TempDir()
	first := writeInstructions(t, dir, "first.md", "FIRST-SMALL\n")
	huge := writeInstructions(t, dir, "huge.md", "HUGE-START\n"+strings.Repeat("h", 9000)+"\n")
	last := writeInstructions(t, dir, "last.md", "LAST-SMALL\n")
	inst := identityTestInstance("codex")
	inst.InstructionsFiles = []string{first, huge, last}

	value, ok := inst.codexIdentityOverride(t.TempDir(), inst.ProjectPath)
	require.True(t, ok)
	require.LessOrEqual(t, len(value), identityInlineMaxBytes)
	got := tomlRoundTrip(t, value)
	require.Contains(t, got, "FIRST-SMALL")
	require.Contains(t, got, "LAST-SMALL")
	require.NotContains(t, got, "HUGE-START")
	require.Contains(t, got, "\n### "+huge+"\n(not included: over the 8 KB inline budget of codex's developer_instructions; read the file itself)\n")
	require.Less(t, strings.Index(got, "FIRST-SMALL"), strings.Index(got, huge))
	require.Less(t, strings.Index(got, huge), strings.Index(got, "LAST-SMALL"))

	file, _ := inst.IdentityFilePath()
	full, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, string(full), "HUGE-START", "the identity file carries the full text")

	_, files := inst.InstructionsFilesReport()
	require.Equal(t, InstructionsStateOK, files[0].State)
	require.Equal(t, InstructionsStateNotDelivered, files[1].State)
	require.Equal(t, InstructionsStateOK, files[2].State)
}

// Inline budget: SSH claude and codex run the real launch through the hop
// shims and check what the harness and the remote file receive.
func TestInstructionsFiles_SSHInlineWithinBudget(t *testing.T) {
	bin := remoteTestEnv(t)
	dir := t.TempDir()
	role := writeInstructions(t, dir, "role.md", "SSH-ROLE 'quoted' $(touch MUST_NOT_EXIST) `x` exec this\n\tTabbed\n")
	huge := writeInstructions(t, dir, "huge.md", "SSH-HUGE\n"+strings.Repeat("y", 7000)+"\n")

	t.Run("claude", func(t *testing.T) {
		record := remoteHarnessRecorder(t, bin, "claude")
		inst := sshIdentityInstance("claude", "remote worker")
		inst.InstructionsFiles = []string{role, huge}
		launch, _, err := inst.prepareCommand(inst.buildClaudeCommand("claude"))
		require.NoError(t, err)
		require.NotContains(t, launch, "\n")
		require.NotContains(t, launch, "\t", "a tab travels encoded")
		runtimeDir := t.TempDir()
		r := runLaunch(t, launch, record, runtimeDir)
		got, ok := r.flagValue("--append-system-prompt")
		require.True(t, ok)
		require.Contains(t, got, "SSH-ROLE 'quoted' $(touch MUST_NOT_EXIST) `x` exec this\n\tTabbed\n")
		require.NotContains(t, got, "SSH-HUGE")
		// $(...) drops the block's trailing newline; the file keeps it.
		require.True(t, strings.HasSuffix(got, "\n### "+huge+"\n(not included: over the 8 KB inline budget of an SSH/sandboxed launch; the file is on the agent-deck host, not where this session runs)"), got)
		want, _ := inst.identityRemoteText()
		assertRemoteFile(t, r, runtimeDir, inst.ID, want)
	})

	t.Run("codex", func(t *testing.T) {
		record := remoteHarnessRecorder(t, bin, "codex")
		inst := sshIdentityInstance("codex", "remote worker")
		inst.InstructionsFiles = []string{role}
		launch, _, err := inst.prepareCommand(inst.buildCodexCommand("codex"))
		require.NoError(t, err)
		r := runLaunch(t, launch, record, t.TempDir())
		override, ok := r.flagValue("-c")
		require.True(t, ok, "codex keeps its -c override with the instructions inside the budget: %q", r.args)
		require.Contains(t, tomlRoundTrip(t, override), "SSH-ROLE 'quoted'")
		require.Contains(t, r.file, "SSH-ROLE")
	})
}

func TestInstructionsFiles_PersistAndClearRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	td := WriteInstructionsFilesToToolData(json.RawMessage(`{"other":1}`), []string{"/a.md", "/b.md"})
	require.Equal(t, []string{"/a.md", "/b.md"}, ReadInstructionsFilesFromToolData(td))
	cleared := WriteInstructionsFilesToToolData(td, nil)
	require.Nil(t, ReadInstructionsFilesFromToolData(cleared))
	require.Contains(t, string(cleared), `"other":1`)

	storage := newTestStorage(t)
	inst := NewInstance("with-instructions", "/tmp")
	inst.Tool = "claude"
	inst.InstructionsFiles = []string{"/team/role.md"}
	save := func() *Instance {
		require.NoError(t, storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)))
		loaded, _, err := storage.LoadWithGroups()
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		return loaded[0]
	}
	require.Equal(t, []string{"/team/role.md"}, save().InstructionsFiles)
	_, _, err := SetField(inst, FieldInstructionsFile, "", nil)
	require.NoError(t, err)
	require.Empty(t, save().InstructionsFiles, "a cleared list must not be resurrected by the extras merge")
}

func TestInstructionsFiles_ForkKeepsSourceFiles(t *testing.T) {
	identityTestEnv(t)
	role := writeInstructions(t, t.TempDir(), "role.md", "FORK-ROLE\n")

	t.Run("claude", func(t *testing.T) {
		parent := &Instance{ID: "parent99-1700000000", Tool: "claude", Title: "Parent", ProjectPath: t.TempDir(),
			ClaudeSessionID: "11111111-1111-4111-8111-111111111111", ClaudeDetectedAt: time.Now()}
		parent.markClaudeSessionIDVerified()
		parent.InstructionsFiles = []string{role}
		forked, cmd, err := parent.CreateForkedInstanceForTool("Child", "", nil)
		require.NoError(t, err)
		require.Equal(t, []string{role}, forked.InstructionsFiles)
		file, _ := forked.IdentityFilePath()
		require.Contains(t, cmd, "--append-system-prompt-file "+file)
		got, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Contains(t, string(got), "FORK-ROLE", "the fork's own block carries the files from its first spawn")
		forked.InstructionsFiles[0] = "/changed"
		require.Equal(t, role, parent.InstructionsFiles[0], "copied, not aliased")
	})

	t.Run("codex", func(t *testing.T) {
		parent := identityTestInstance("codex")
		parent.ProjectPath = t.TempDir()
		parent.CodexSessionID = "019a3206-0000-7000-8000-000000000001"
		parent.InstructionsFiles = []string{role}
		writeRollout(t, CodexHomeDirForInstance(parent), parent.CodexSessionID, parent.ProjectPath, "", "", time.Now())
		forked, cmd, err := parent.CreateForkedInstanceForTool("Child", "", nil)
		require.NoError(t, err)
		require.Equal(t, []string{role}, forked.InstructionsFiles)
		require.Contains(t, cmd, "FORK-ROLE")
	})
}
