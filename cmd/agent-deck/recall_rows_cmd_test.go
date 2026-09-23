package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// rowsTestSession adds a Claude session to an isolated home and writes its
// native transcript where Claude Code would. [recall] is enabled (the gate)
// but nothing is ever indexed: rows come from the native file.
func rowsTestSession(t *testing.T) (home, id, transcript string) {
	t.Helper()
	home = t.TempDir()
	writeMacappConfig(t, home, "[recall]\nenabled = true\nmax_loadavg = 0\n")
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "rows-cli", "-c", "claude", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add JSON: %v %s", err, stdout)
	}
	const claudeID = "11111111-2222-3333-4444-555555555555"
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", added.ID, "claude-session-id", claudeID); code != 0 {
		t.Fatalf("set claude-session-id: %d %s %s", code, stdout, stderr)
	}
	resolved := project
	if r, err := filepath.EvalSymlinks(project); err == nil {
		resolved = r
	}
	dir := filepath.Join(home, ".claude", "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "recall", "query", "testdata", "rows", "claude-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	transcript = filepath.Join(dir, claudeID+".jsonl")
	if err := os.WriteFile(transcript, []byte(strings.Join(lines[:10], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, added.ID, transcript
}

type rowsTimelineJSON struct {
	Schema  string `json:"schema"`
	Source  string `json:"source"`
	Session struct {
		ID       string `json:"id"`
		Harness  string `json:"harness"`
		NativeID string `json:"native_id"`
		Path     string `json:"path"`
		Title    string `json:"title"`
		Cwd      string `json:"cwd"`
	} `json:"session"`
	Turns []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"turns"`
	ThroughCursor string `json:"through_cursor"`
	Status        *struct {
		SessionID     string `json:"session_id"`
		SessionStatus string `json:"session_status"`
	} `json:"status"`
}

// TestRecallRowsWithoutIndex: a deck session's transcript is read directly
// with no index at all, by deck id, by title and by its Claude conversation
// id; the gate still applies.
func TestRecallRowsWithoutIndex(t *testing.T) {
	home, id, transcript := rowsTestSession(t)
	for _, ref := range []string{id, "rows-cli", "11111111-2222-3333-4444-555555555555"} {
		stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", ref, "--json")
		if code != 0 {
			t.Fatalf("timeline %s: %d %s %s", ref, code, stdout, stderr)
		}
		var tl rowsTimelineJSON
		if err := json.Unmarshal([]byte(stdout), &tl); err != nil {
			t.Fatalf("JSON: %v %s", err, stdout)
		}
		if tl.Schema != "agent-deck.recall.rows/v2" || tl.Source != "native" || tl.Session.ID != id || tl.Session.Harness != "claude" ||
			tl.Session.NativeID != "11111111-2222-3333-4444-555555555555" || tl.Session.Path != transcript || tl.Session.Title != "rows-cli" || tl.Session.Cwd == "" ||
			tl.ThroughCursor == "" || len(tl.Turns) == 0 {
			t.Fatalf("timeline %s: %+v", ref, tl)
		}
		if tl.Status == nil || tl.Status.SessionID != id || tl.Status.SessionStatus == "" {
			t.Fatalf("timeline carries no status: %s", stdout)
		}
	}
	// --tail N answers the last rows; --v1 keeps the slice-6 shape.
	stdout, _, code := runAgentDeck(t, home, "recall", "timeline", id, "--json", "--tail", "2")
	var tl rowsTimelineJSON
	if code != 0 || json.Unmarshal([]byte(stdout), &tl) != nil || len(tl.Turns) != 2 {
		t.Fatalf("--tail 2: %d %s", code, stdout)
	}
	// Gate: with [recall] off nothing is served.
	writeMacappConfig(t, home, "[recall]\nenabled = false\n")
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", id, "--json"); code != 2 {
		t.Fatalf("timeline bypassed the recall gate: exit %d", code)
	}
}

func TestRecallRowsTranscriptFlagAndErrors(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[recall]\nenabled = true\nmax_loadavg = 0\n")
	fixture, _ := filepath.Abs(filepath.Join("..", "..", "internal", "recall", "query", "testdata", "rows", "codex-rollout.jsonl"))
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--transcript", fixture, "--harness", "codex")
	if code != 0 || !strings.Contains(stdout, `"kind": "turn_end"`) {
		t.Fatalf("--transcript: %d %s %s", code, stdout, stderr)
	}
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "--json", "--transcript", fixture); code == 0 {
		t.Fatal("--transcript without --harness accepted")
	}
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "no-such-session", "--json"); code == 0 {
		t.Fatal("unknown session accepted")
	}
	stdout, stderr, _ = runAgentDeck(t, home, "recall", "timeline", "--help")
	for _, flag := range []string{"--since", "--limit", "--tail", "--v1"} {
		if !strings.Contains(stdout+stderr, flag) {
			t.Fatalf("help does not document %s: %s %s", flag, stdout, stderr)
		}
	}
}

// TestRecallRowsFollowCLI: follow from the timeline cursor streams an
// appended mid-turn message as a queued user row within 500 ms, and its
// absorption as an update with queued:false at the absorb time.
func TestRecallRowsFollowCLI(t *testing.T) {
	home, id, transcript := rowsTestSession(t)
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", id, "--json")
	if code != 0 {
		t.Fatalf("timeline: %d %s %s", code, stdout, stderr)
	}
	var tl rowsTimelineJSON
	if err := json.Unmarshal([]byte(stdout), &tl); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(channelsCLIBinary(t), "recall", "follow", id, "--after", tl.ThroughCursor, "--jsonl", "--status")
	cmd.Env = agentDeckTestEnv(home, nil)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	frames := make(chan map[string]any, 64)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var f map[string]any
			if json.Unmarshal(sc.Bytes(), &f) == nil {
				frames <- f
			}
		}
		close(frames)
	}()
	// The first status frame proves the follower is up.
	select {
	case f := <-frames:
		if f["frame"] != "status" || f["session_id"] != id {
			t.Fatalf("first frame: %v", f)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no status frame")
	}
	enqueue := `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-23T09:00:00.000Z","content":"typed while busy"}` + "\n"
	remove := `{"type":"queue-operation","operation":"remove","timestamp":"2026-09-23T09:00:05.000Z","content":"typed while busy","reason":"absorbed_mid_turn"}` + "\n"
	appended := time.Now()
	appendFileCLI(t, transcript, enqueue)
	var seen []string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("follow exited; frames %v", seen)
			}
			typ, _ := f["frame"].(string)
			if typ == "status" {
				continue
			}
			seen = append(seen, typ)
			if typ == "resync_required" {
				t.Fatalf("resync: %v", f)
			}
			row, _ := f["row"].(map[string]any)
			if len(seen) == 1 {
				if lat := time.Since(appended); lat > 500*time.Millisecond {
					t.Errorf("appended line took %v to stream (budget 500ms)", lat)
				}
				if typ != "row" || row["queued"] != true || row["kind"] != "user" || f["cursor"] == nil {
					t.Fatalf("queued frame: %v", f)
				}
				appendFileCLI(t, transcript, remove)
			}
			if len(seen) == 2 {
				if typ != "update" || row["queued"] != false || row["ts"] != "2026-09-23T09:00:05.000Z" {
					t.Fatalf("absorb frame: %v", f)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out; frames %v", seen)
		}
	}
}

func appendFileCLI(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}
