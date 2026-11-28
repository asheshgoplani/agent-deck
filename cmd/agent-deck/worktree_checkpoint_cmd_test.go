package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorktreeCheckpointCLI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	bin := goldensBinary(t)
	home, env := goldensSandbox(t)
	source := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = source, env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	git("init", "-q")
	file := filepath.Join(source, "work.txt")
	require.NoError(t, os.WriteFile(file, []byte("base\n"), 0o600))
	git("add", ".")
	git("-c", "user.name=Checkpoint", "-c", "user.email=checkpoint@example.invalid", "commit", "-qm", "base")
	require.NoError(t, os.WriteFile(file, []byte("partial fix\n"), 0o600))
	outPath := filepath.Join(home, "checkpoint.json")
	note := filepath.Join(home, "note.txt")
	require.NoError(t, os.WriteFile(note, []byte("run the test next\n"), 0o600))
	run := func(args ...string) (string, string, int) {
		return runGoldensStreamsIn(t, bin, env, source, append([]string{"worktree", "checkpoint"}, args...))
	}
	_, stderr, code := run("save", "--output", outPath, "--note", note)
	require.Zero(t, code, "%s", stderr)
	stdout, stderr, code := run("preview", outPath)
	require.Zero(t, code, "%s", stderr)
	require.Contains(t, stdout, `"work.txt"`)
	require.Contains(t, stdout, `"run the test next\n"`)
	var sha string
	for _, line := range strings.Split(stdout, "\n") {
		if s, ok := strings.CutPrefix(line, "SHA256: "); ok {
			sha = s
		}
	}
	require.Len(t, sha, 64)
	target := filepath.Join(t.TempDir(), "restored")
	_, stderr, code = run("restore", outPath, "--path", target, "--expect", "wrong")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "checksum")
	require.NoDirExists(t, target)
	_, stderr, code = run("restore", outPath, "--path", target, "--expect", sha)
	require.Zero(t, code, "%s", stderr)
	data, err := os.ReadFile(filepath.Join(target, "work.txt"))
	require.NoError(t, err)
	require.Equal(t, "partial fix\n", string(data))
}
