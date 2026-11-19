package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Issue #3206: per-session instruction files.
//
// A session carries a list of absolute file paths. At EVERY spawn (start,
// restart, resume, fork) agent-deck reads each file and appends its text as
// its own section to the identity block it already delivers: claude gets it
// through `--append-system-prompt-file <identity file>`, codex through
// `-c developer_instructions="<block>"`.
//
// This test drives the real lifecycle end to end against real (isolated)
// tmux with a fake harness binary that records what it was handed:
//
//  1. set the instructions file on a session,
//  2. persist it through SQLite and reload it into a fresh Instance (the
//     `session restart` from another process / after a reboot shape),
//  3. Start: the harness sees MARKER-ONE,
//  4. edit the file, Restart: the harness sees MARKER-TWO (read per spawn),
//  5. stage a resumable conversation, persist + reload again, Restart: the
//     spawn really resumes AND the harness still sees MARKER-TWO.

const issue3206FieldInstructionsFile = "instructions-file"

// issue3206FakeHarness writes a fake `claude` / `codex` into binDir that
// records each spawn under recDir:
//
//	argv-<n>      NUL-separated argv (written last, so it marks completion)
//	identity-<n>  the identity text the harness was given (claude: content
//	              of the --append-system-prompt-file file; codex: the
//	              developer_instructions=... override verbatim)
//
// The process then stays alive for a bounded ~60s so tmux keeps the pane;
// every test session is killed in t.Cleanup well before that.
func issue3206FakeHarness(t *testing.T, binDir, recDir, tool string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(recDir, 0o755))
	var capture string
	switch tool {
	case "claude":
		capture = `
prev=""
for a in "$@"; do
  if [ "$prev" = "--append-system-prompt-file" ]; then
    cat "$a" >> "$dir/identity-$n.tmp" 2>/dev/null
  fi
  prev="$a"
done`
	case "codex":
		capture = `
prev=""
for a in "$@"; do
  if [ "$prev" = "-c" ]; then
    case "$a" in
      developer_instructions=*) printf '%s' "$a" >> "$dir/identity-$n.tmp" ;;
    esac
  fi
  prev="$a"
done`
	default:
		t.Fatalf("unsupported tool %q", tool)
	}
	script := "#!/bin/sh\n" +
		"dir=" + shellescape.Quote(recDir) + "\n" +
		"n=1\n" +
		"while [ -e \"$dir/argv-$n\" ] || [ -e \"$dir/argv-$n.tmp\" ]; do n=$((n+1)); done\n" +
		": > \"$dir/identity-$n.tmp\"\n" +
		capture + "\n" +
		"mv \"$dir/identity-$n.tmp\" \"$dir/identity-$n\"\n" +
		"printf '%s\\0' \"$@\" > \"$dir/argv-$n.tmp\"\n" +
		"mv \"$dir/argv-$n.tmp\" \"$dir/argv-$n\"\n" +
		"i=0\n" +
		"while [ $i -lt 60 ]; do sleep 1; i=$((i+1)); done\n"
	path := filepath.Join(binDir, tool)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// issue3206Spawn is one recorded harness invocation.
type issue3206Spawn struct {
	argv     []string
	identity string
}

// issue3206WaitSpawn waits (bounded) for spawn number n to be recorded.
func issue3206WaitSpawn(t *testing.T, recDir string, n int, timeout time.Duration) issue3206Spawn {
	t.Helper()
	argvPath := filepath.Join(recDir, fmt.Sprintf("argv-%d", n))
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(argvPath); err == nil {
			ident, _ := os.ReadFile(filepath.Join(recDir, fmt.Sprintf("identity-%d", n)))
			argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			return issue3206Spawn{argv: argv, identity: string(ident)}
		}
		if time.Now().After(deadline) {
			entries, _ := os.ReadDir(recDir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("harness spawn #%d not recorded within %s (record dir holds %v)", n, timeout, names)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// issue3206ShortArgv shortens long argv entries (the codex identity
// override) so the logs stay readable.
func issue3206ShortArgv(argv []string) []string {
	out := make([]string, len(argv))
	for k, a := range argv {
		if len(a) > 80 {
			a = a[:40] + "..."
		}
		out[k] = a
	}
	return out
}

func issue3206ArgvHasPair(argv []string, a, b string) bool {
	for k := 0; k+1 < len(argv); k++ {
		if argv[k] == a && argv[k+1] == b {
			return true
		}
	}
	return false
}

func issue3206ArgvHas(argv []string, a string) bool {
	for _, v := range argv {
		if v == a {
			return true
		}
	}
	return false
}

// issue3206Reload saves inst through real SQLite storage and loads it back
// into a fresh Instance, the way another agent-deck process would see it.
func issue3206Reload(t *testing.T, storage *Storage, inst *Instance) *Instance {
	t.Helper()
	require.NoError(t, storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)))
	loaded, _, err := storage.LoadWithGroups()
	require.NoError(t, err)
	for _, l := range loaded {
		if l.ID == inst.ID {
			require.NotSame(t, inst, l)
			// Every instance that may own the tmux session is killed at cleanup.
			t.Cleanup(func() { _ = l.Kill() })
			return l
		}
	}
	t.Fatalf("session %s missing after SQLite round trip", inst.ID)
	return nil
}

func TestIssue3206_InstructionsFileDeliveredAtStartRestartResume(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			testutil.RequireTmux(t)

			home := isolatedHomeDir(t)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CODEX_HOME", "")
			t.Setenv("GEMINI_CLI_HOME", "")
			t.Setenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH", "")
			ClearUserConfigCache()

			binDir := filepath.Join(home, "bin")
			recDir := filepath.Join(home, "record")
			stub := issue3206FakeHarness(t, binDir, recDir, tool)
			// The fake is first on PATH for this process; the pane's PATH
			// comes from the isolated tmux server's environment, so the
			// configured command names it by absolute path as well.
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cfgPath, err := GetUserConfigPath()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
			require.NoError(t, os.WriteFile(cfgPath,
				[]byte(fmt.Sprintf("[%s]\ncommand = %q\n", tool, stub)), 0o644))
			ClearUserConfigCache()

			projectPath := filepath.Join(home, "project")
			require.NoError(t, os.MkdirAll(projectPath, 0o755))

			instructions := filepath.Join(t.TempDir(), "role.md")
			require.NoError(t, os.WriteFile(instructions, []byte("ROLE MARKER-ONE: you review only.\n"), 0o644))

			created := NewInstanceWithTool("issue3206-"+tool, projectPath, tool)
			if tool == "codex" {
				waitCodexDetectionDone(t)
			}
			t.Cleanup(func() { _ = created.Kill() })

			_, postCommit, err := SetField(created, issue3206FieldInstructionsFile, instructions, nil)
			require.NoError(t, err, "SetField(%q) must accept an absolute instructions file path", issue3206FieldInstructionsFile)
			if postCommit != nil {
				postCommit()
			}

			storage := newTestStorage(t)
			inst := issue3206Reload(t, storage, created)

			// --- start ---
			require.NoError(t, inst.Start())
			s1 := issue3206WaitSpawn(t, recDir, 1, 15*time.Second)
			t.Logf("start argv: %q", issue3206ShortArgv(s1.argv))
			require.NotEmpty(t, s1.identity, "start: harness got no identity block at all")
			require.Contains(t, s1.identity, "You are running inside agent-deck", "start: identity block missing")
			require.Contains(t, s1.identity, "ROLE MARKER-ONE: you review only.",
				"start: instructions file text must be part of the identity block")

			// --- restart picks up the edited file ---
			require.NoError(t, os.WriteFile(instructions, []byte("ROLE MARKER-TWO: you review only.\n"), 0o644))
			require.NoError(t, inst.Restart())
			s2 := issue3206WaitSpawn(t, recDir, 2, 15*time.Second)
			t.Logf("restart argv: %q", issue3206ShortArgv(s2.argv))
			require.Contains(t, s2.identity, "You are running inside agent-deck", "restart: identity block missing")
			require.Contains(t, s2.identity, "ROLE MARKER-TWO", "restart: the file must be re-read at every spawn")
			require.NotContains(t, s2.identity, "MARKER-ONE", "restart: stale instructions delivered")

			// --- resume ---
			var resumeID string
			switch tool {
			case "claude":
				resumeID = inst.ClaudeSessionID
				require.NotEmpty(t, resumeID, "claude start must have minted a conversation id")
				stageConversation(t, home, inst.ProjectPath, resumeID)
			case "codex":
				resumeID = "019a3206-0000-7000-8000-000000003206"
				inst.CodexSessionID = resumeID
				inst.CodexDetectedAt = time.Now()
				writeRollout(t, CodexHomeDirForInstance(inst), resumeID, inst.ProjectPath, "", "", time.Now())
			}
			resumed := issue3206Reload(t, storage, inst)
			require.NoError(t, resumed.Restart())
			s3 := issue3206WaitSpawn(t, recDir, 3, 15*time.Second)
			t.Logf("resume argv: %q", issue3206ShortArgv(s3.argv))
			switch tool {
			case "claude":
				require.True(t, issue3206ArgvHasPair(s3.argv, "--resume", resumeID),
					"restart did not resume the recorded conversation: %q", s3.argv)
			case "codex":
				require.True(t, issue3206ArgvHas(s3.argv, "resume") && issue3206ArgvHas(s3.argv, resumeID),
					"restart did not run `codex resume <id>`: %q", s3.argv)
			}
			require.Contains(t, s3.identity, "You are running inside agent-deck", "resume: identity block missing")
			require.Contains(t, s3.identity, "ROLE MARKER-TWO", "resume: instructions must be delivered on resume too")
		})
	}
}
