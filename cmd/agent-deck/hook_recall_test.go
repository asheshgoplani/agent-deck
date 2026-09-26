package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/testcorpus"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// hookHome isolates HOME and the XDG dirs, writes a config with [recall]
// enabled (when enabled) and a work account slot, and returns the home
// plus the personal (~/.claude) and work config dirs.
func hookHome(t *testing.T, enabled bool) (home, personal, work string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "personal")
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, filepath.Join(home, strings.ToLower(name)))
	}
	personal = filepath.Join(home, ".claude")
	work = filepath.Join(home, ".claude-work")
	cfgDir := filepath.Join(home, "xdg_config_home", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[recall]\nenabled = %v\nmax_loadavg = 0\n\n[profiles.personal.claude]\nconfig_dir = %q\n\n[profiles.work.claude]\nconfig_dir = %q\n", enabled, personal, work)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home, personal, work
}

// assistantTurn is a transcript whose assistant record is followed by the
// system and sidechain records Claude Code appends after a turn.
const assistantTurn = `{"type":"user","message":{"role":"user","content":"clock skew"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"%s","cwd":"/tmp/p"}
{"type":"assistant","message":{"role":"assistant","model":"claude-test","content":[{"type":"text","text":"The root cause was clock skew."}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}},"uuid":"a1","timestamp":"2026-09-19T10:00:01.000Z","sessionId":"%s"}
{"type":"system","subtype":"turn_duration","durationMs":1000,"uuid":"s1","timestamp":"2026-09-19T10:00:02.000Z"}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","model":"claude-sub","content":[{"type":"text","text":"subagent"}],"usage":{"input_tokens":999,"output_tokens":999}},"uuid":"sa1","timestamp":"2026-09-19T10:00:03.000Z","sessionId":"%s"}
{"type":"attachment","attachment":{"type":"diagnostics"},"uuid":"at1"}
`

func writeTurn(t *testing.T, path, sid string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(assistantTurn, sid, sid, sid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func costEventFiles(t *testing.T) []string {
	t.Helper()
	entries, _ := os.ReadDir(getCostEventsDir())
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestWriteCostEvent_ContainmentAndTailWalk pins the Claude hook containment
// fix: the cost event is written for a transcript reported under any
// Claude config dir agent-deck can launch under (the personal dir, a
// configured account slot, a worker-scratch home whose projects/ symlinks
// into one of them) and even when the assistant record is not the literal
// last line; a spoofed sibling dir and a symlink escaping every root are
// refused.
func TestWriteCostEvent_ContainmentAndTailWalk(t *testing.T) {
	initTestLogging(t)
	home, personal, work := hookHome(t, true)
	scratchRoot, err := session.WorkerScratchDirRoot()
	if err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(scratchRoot, "inst-1", "generation-7")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "projects"), filepath.Join(gen, "projects")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "elsewhere", "projects", "p", "cccccccc-0000-4000-8000-000000000003.jsonl")
	writeTurn(t, outside, "cccccccc-0000-4000-8000-000000000003")
	escape := filepath.Join(personal, "projects", "p", "escape.jsonl")
	if err := os.MkdirAll(filepath.Dir(escape), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	spoof := filepath.Join(home, ".claude-spoof", "projects", "p", "dddddddd-0000-4000-8000-000000000004.jsonl")
	writeTurn(t, spoof, "dddddddd-0000-4000-8000-000000000004")

	cases := []struct {
		name string
		path string
		sid  string
		want bool
	}{
		{"personal dir, assistant not last line", filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000001.jsonl"), "aaaaaaaa-0000-4000-8000-000000000001", true},
		{"work account slot", filepath.Join(work, "projects", "p", "bbbbbbbb-0000-4000-8000-000000000002.jsonl"), "bbbbbbbb-0000-4000-8000-000000000002", true},
		{"worker-scratch symlinked projects", filepath.Join(gen, "projects", "p", "bbbbbbbb-0000-4000-8000-000000000002.jsonl"), "", true},
		{"spoofed sibling dir", spoof, "", false},
		{"symlink escaping every root", escape, "", false},
		{"outside every root", outside, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sid != "" {
				writeTurn(t, tc.path, tc.sid)
			}
			before := len(costEventFiles(t))
			payload := []byte(`{"hook_event_name":"Stop","transcript_path":` + fmt.Sprintf("%q", tc.path) + `}`)
			writeCostEvent("inst-1", payload)
			got := len(costEventFiles(t)) - before
			if (got == 1) != tc.want {
				t.Fatalf("cost events written = %d, want written=%v", got, tc.want)
			}
			if tc.want {
				files := costEventFiles(t)
				data, _ := os.ReadFile(filepath.Join(getCostEventsDir(), files[len(files)-1]))
				if !strings.Contains(string(data), `"input_tokens":10`) || strings.Contains(string(data), "claude-sub") {
					t.Fatalf("wrong record charged (sidechain or stale): %s", data)
				}
			}
		})
	}
}

// TestRecallHookTrigger_QueuesAndIndexesOnlyThatFile: a Stop hook queues
// the transcript and indexes exactly that file; other transcripts in the
// same tree are left for the sweep; a path outside the roots and a
// disabled index leave no trace.
func TestRecallHookTrigger_QueuesAndIndexesOnlyThatFile(t *testing.T) {
	initTestLogging(t)
	// The 150 ms interactive budget also covers opening the profile's
	// state.db; under -race on a loaded runner that can leave this file
	// partly indexed (resumed by the next sweep, by design), which made the
	// exact message counts below flaky. This test is about which file gets
	// indexed, not about the budget.
	previousDeadline := recallHookDeadline
	recallHookDeadline = 30 * time.Second
	t.Cleanup(func() { recallHookDeadline = previousDeadline })
	home, personal, _ := hookHome(t, true)
	if _, err := testcorpus.Generate(personal, testcorpus.Options{Files: 3, Seed: 5}); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000001.jsonl")
	writeTurn(t, mine, "aaaaaaaa-0000-4000-8000-000000000001")
	queue, err := recall.QueuePath()
	if err != nil {
		t.Fatal(err)
	}
	dbPath, err := recall.DBPath()
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(home, "elsewhere", "x.jsonl")
	writeTurn(t, outside, "x")
	recallHookTrigger("inst-1", "Stop", []byte(`{"hook_event_name":"Stop","transcript_path":`+fmt.Sprintf("%q", outside)+`}`))
	if _, err := os.Stat(queue); !os.IsNotExist(err) {
		t.Fatal("a path outside every root must not be queued")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("a refused path must not open the index")
	}
	recallHookTrigger("inst-1", "PreToolUse", []byte(`{"hook_event_name":"PreToolUse","transcript_path":`+fmt.Sprintf("%q", mine)+`}`))
	if _, err := os.Stat(queue); !os.IsNotExist(err) {
		t.Fatal("only Stop and SessionEnd trigger recall")
	}

	// Stop is synchronous on Claude's turn end: it appends the queue line
	// and touches nothing else (no recall.db open, no lock, no sweep).
	started := time.Now()
	recallHookTrigger("inst-1", "Stop", []byte(`{"hook_event_name":"Stop","transcript_path":`+fmt.Sprintf("%q", mine)+`}`))
	stopTook := time.Since(started)
	if n := recall.QueueLen(queue); n != 1 {
		t.Fatalf("queue lines = %d", n)
	}
	data, _ := os.ReadFile(queue)
	if !strings.Contains(string(data), mine) || !strings.Contains(string(data), `"instance":"inst-1"`) || !strings.Contains(string(data), `"harness":"claude"`) {
		t.Fatalf("queue line: %s", data)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("the Stop hook must not open recall.db")
	}
	lockPath, _ := recall.LockPath()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("the Stop hook must not take the sweep lock")
	}
	if stopTook > 50*time.Millisecond {
		t.Fatalf("Stop path took %s; it must stay under 50 ms", stopTook)
	}

	// SessionEnd is asynchronous: it queues and, with hook_sweep on,
	// indexes exactly this file.
	recallHookTrigger("inst-1", "SessionEnd", []byte(`{"hook_event_name":"SessionEnd","transcript_path":`+fmt.Sprintf("%q", mine)+`}`))
	if n := recall.QueueLen(queue); n != 2 {
		t.Fatalf("queue lines after SessionEnd = %d", n)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sources, sessions, msgs int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM source), (SELECT count(*) FROM session), (SELECT count(*) FROM msg)`).Scan(&sources, &sessions, &msgs); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || sessions != 1 || msgs != 3 {
		t.Fatalf("indexed sources=%d sessions=%d msgs=%d: the hook must index only its own file", sources, sessions, msgs)
	}
	var path string
	if err := db.QueryRow(`SELECT path FROM source`).Scan(&path); err != nil || path != mine {
		t.Fatalf("source path %q (%v) want %q", path, err, mine)
	}
	// Repeating the hook is a no-op on the index and one more queue line.
	recallHookTrigger("inst-1", "SessionEnd", []byte(`{"hook_event_name":"SessionEnd","transcript_path":`+fmt.Sprintf("%q", mine)+`}`))
	if err := db.QueryRow(`SELECT count(*) FROM msg`).Scan(&msgs); err != nil || msgs != 3 {
		t.Fatalf("msgs after repeat = %d (%v)", msgs, err)
	}
	if n := recall.QueueLen(queue); n != 3 {
		t.Fatalf("queue lines after repeat = %d", n)
	}
}

func TestRecallHookTrigger_OffByDefaultDoesNothing(t *testing.T) {
	initTestLogging(t)
	_, personal, _ := hookHome(t, false)
	mine := filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000001.jsonl")
	writeTurn(t, mine, "aaaaaaaa-0000-4000-8000-000000000001")
	recallHookTrigger("inst-1", "Stop", []byte(`{"hook_event_name":"Stop","transcript_path":`+fmt.Sprintf("%q", mine)+`}`))
	queue, _ := recall.QueuePath()
	dbPath, _ := recall.DBPath()
	for _, p := range []string{queue, dbPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists with [recall] enabled = false", p)
		}
	}
}
