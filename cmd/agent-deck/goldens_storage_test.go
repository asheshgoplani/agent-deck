package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestStorageBytesGoldens is PROMPT.md deliverable 2: storage-bytes goldens
// after each of session start, stop, restart, list, group list, against a
// private tmux server (local-only-common.md's sandbox recipe), driven purely
// by shell commands (the built binary + tmux) — no agent involved.
//
// Only golden-sess-shell (a Tool="shell" instance, so starting it runs a
// plain shell and needs no claude/codex/gemini binary on the test box) is
// touched by session start/stop/restart. list and group list run against
// the full seeded store. After every action, every instances/groups row is
// dumped as canonical JSON (id-ordered) and scrubbed the same way as the CLI
// goldens (testdata/goldens/README.md).
func TestStorageBytesGoldens(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("tmux is required for storage goldens: %v", err)
	}
	bin := goldensBinary(t)
	home, env := goldensSandbox(t)

	// Private tmux server: tmux's client discovery is $TMUX -> -S -> -L ->
	// $TMUX_TMPDIR, so a fresh TMUX_TMPDIR with TMUX/TMUX_PANE unset is
	// enough to isolate the default socket name to this test (see
	// internal/testutil.IsolateTmuxSocket, which this mirrors for a
	// subprocess env instead of the test process's own env).
	tmuxTmpdir := t.TempDir()
	env = filterEnv(env, "TMUX", "TMUX_PANE", "TMUX_TMPDIR")
	env = append(env, "TMUX_TMPDIR="+tmuxTmpdir)
	t.Cleanup(func() {
		testutil.KillTmuxServersUnder(tmuxTmpdir)
		// A failed kill must be visible, including on an earlier test failure.
		_ = filepath.WalkDir(tmuxTmpdir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				t.Errorf("checking private tmux socket: %v", walkErr)
				return nil
			}
			if info, err := entry.Info(); err == nil && info.Mode()&os.ModeSocket != 0 {
				t.Errorf("private tmux socket leaked: %s", path)
			}
			return nil
		})
	})

	profileDir, err := session.GetProfileDir(goldensProfile)
	if err != nil {
		t.Fatalf("resolving profile dir: %v", err)
	}
	dbPath := filepath.Join(profileDir, "state.db")

	// A dedicated Tool="shell" fixture row, separate from the six goldens_test.go
	// seeds, so the CLI goldens (which assert the exact seeded set) are never
	// perturbed by this test mutating a session's status/tmux fields.
	seedShellInstance(t, dbPath, home)

	dumpAndAssert := func(step string) {
		t.Helper()
		got := dumpStateDBRows(t, dbPath)
		assertGolden(t, "storage_"+step, home, got)
	}

	dumpAndAssert("00_seeded")

	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-p", goldensProfile}, args...)
		stdout, exit := runGoldens(t, bin, env, full)
		t.Logf("agent-deck %s (exit %d):\n%s", strings.Join(args, " "), exit, stdout)
		if exit != 0 {
			t.Fatalf("agent-deck %s: exit %d\n%s", strings.Join(args, " "), exit, stdout)
		}
	}

	run("session", "start", "golden-sess-shell", "--no-wait")
	waitForStatus(t, bin, env, "golden-sess-shell", []string{"running", "starting", "idle"}, 10*time.Second)
	dumpAndAssert("01_after_start")

	run("session", "stop", "golden-sess-shell")
	waitForStatus(t, bin, env, "golden-sess-shell", []string{"stopped"}, 10*time.Second)
	dumpAndAssert("02_after_stop")

	run("session", "restart", "golden-sess-shell")
	waitForStatus(t, bin, env, "golden-sess-shell", []string{"running", "starting", "idle"}, 10*time.Second)
	dumpAndAssert("03_after_restart")

	run("list")
	dumpAndAssert("04_after_list")

	run("group", "list")
	dumpAndAssert("05_after_group_list")

	run("session", "stop", "golden-sess-shell")
	waitForStatus(t, bin, env, "golden-sess-shell", []string{"stopped"}, 10*time.Second)
}

// filterEnv drops any entry in env whose key is in drop, so a caller can
// override HOME/tmux-discovery vars without the subprocess also inheriting
// this test binary's own (possibly conflicting) values for them.
func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := strings.SplitN(kv, "=", 2)[0]
		skip := false
		for _, d := range drop {
			if key == d {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

func seedShellInstance(t *testing.T, dbPath, home string) {
	t.Helper()

	// A real, existing directory under the sandbox HOME (not t.TempDir(),
	// which lands under the OS default temp base, not under home) — scrub()
	// only normalizes paths under home, and this row is the one fixture row
	// that is actually `session start`ed, so tmux needs somewhere real to cd into.
	projectPath := filepath.Join(home, "shell-fixture-project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("creating shell fixture project dir: %v", err)
	}

	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("opening state.db for shell fixture: %v", err)
	}
	defer db.Close()

	row := &statedb.InstanceRow{
		ID:          "golden-sess-shell",
		Title:       "storage bytes shell",
		ProjectPath: projectPath,
		GroupPath:   "my-sessions",
		Command:     "",
		Tool:        "shell",
		Status:      string(session.StatusStopped),
		// TmuxSession must be non-empty: Storage.LoadWithGroups only builds a
		// *tmux.Session for an instance (Instance.tmuxSession) when this field
		// is set (internal/session/storage.go), and Instance.Start() refuses
		// to run at all without one ("tmux session not initialized").
		TmuxSession:  "ad-golden-sess-shell",
		CreatedAt:    goldensFixedNow,
		LastAccessed: goldensFixedNow,
		ToolData:     []byte(`{}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatalf("seeding shell fixture: %v", err)
	}
}

// waitForStatus polls `session show --json <id>` (which, like the TUI,
// re-derives status from the live tmux pane via RefreshInstancesForCLIStatus
// rather than trusting a possibly-stale DB row) until the id's status
// matches one of want, or the deadline passes. session start/stop/restart
// return once the tmux process is spawned/killed, not once a status refresh
// has run, so the dump right after a command can otherwise race it and make
// the golden flaky.
func waitForStatus(t *testing.T, bin string, env []string, id string, want []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		stdout, exit := runGoldens(t, bin, env, []string{"-p", goldensProfile, "session", "show", id, "--json"})
		if exit == 0 {
			var doc map[string]any
			if err := json.Unmarshal([]byte(stdout), &doc); err == nil {
				if s, ok := doc["status"].(string); ok {
					last = s
					for _, w := range want {
						if last == w {
							return
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s status = %q after %s, want one of %v", id, last, timeout, want)
}

// dumpStateDBRows is the "canonical JSON dump of state.db rows touched"
// deliverable 2 asks for: every instances row and every groups row,
// id/path-ordered so the dump is byte-stable across runs regardless of
// SQLite's physical row order.
func dumpStateDBRows(t *testing.T, dbPath string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatalf("opening state.db for dump: %v", err)
	}
	defer db.Close()

	type dump struct {
		Instances []map[string]any `json:"instances"`
		Groups    []map[string]any `json:"groups"`
	}
	out := dump{}

	rows, err := db.Query("SELECT * FROM instances ORDER BY id")
	if err != nil {
		t.Fatalf("querying instances: %v", err)
	}
	instCols, err := rows.Columns()
	if err != nil {
		t.Fatalf("instances columns: %v", err)
	}
	for rows.Next() {
		vals := make([]any, len(instCols))
		ptrs := make([]any, len(instCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scanning instance row: %v", err)
		}
		m := map[string]any{}
		for i, c := range instCols {
			m[c] = vals[i]
		}
		out.Instances = append(out.Instances, m)
	}
	rows.Close()

	grows, err := db.Query("SELECT * FROM groups ORDER BY path")
	if err != nil {
		t.Fatalf("querying groups: %v", err)
	}
	groupCols, err := grows.Columns()
	if err != nil {
		t.Fatalf("groups columns: %v", err)
	}
	for grows.Next() {
		vals := make([]any, len(groupCols))
		ptrs := make([]any, len(groupCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := grows.Scan(ptrs...); err != nil {
			t.Fatalf("scanning group row: %v", err)
		}
		m := map[string]any{}
		for i, c := range groupCols {
			m[c] = vals[i]
		}
		out.Groups = append(out.Groups, m)
	}
	grows.Close()

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshaling dump: %v", err)
	}
	return string(b) + "\n"
}
