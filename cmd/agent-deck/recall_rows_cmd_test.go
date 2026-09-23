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
// native transcript where Claude Code would, with recall left disabled.
func rowsTestSession(t *testing.T) (home, id, transcript string) {
	t.Helper()
	home = t.TempDir()
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
		ID      string `json:"id"`
		Harness string `json:"harness"`
	} `json:"session"`
	Rows []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"rows"`
	ThroughCursor string `json:"through_cursor"`
	Status        *struct {
		State string `json:"state"`
	} `json:"status"`
}

// TestRecallRowsWithoutIndexOrGate: a deck session's transcript is read
// directly, with [recall] disabled and no index at all, by id and by title.
func TestRecallRowsWithoutIndexOrGate(t *testing.T) {
	home, id, _ := rowsTestSession(t)
	for _, ref := range []string{id, "rows-cli"} {
		stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", ref, "--rows", "--json")
		if code != 0 {
			t.Fatalf("timeline %s: %d %s %s", ref, code, stdout, stderr)
		}
		var tl rowsTimelineJSON
		if err := json.Unmarshal([]byte(stdout), &tl); err != nil {
			t.Fatalf("JSON: %v %s", err, stdout)
		}
		if tl.Schema != "agent-deck.recall.rows/v2" || tl.Source != "native" || tl.Session.ID != id || tl.Session.Harness != "claude" || tl.ThroughCursor == "" || len(tl.Rows) == 0 {
			t.Fatalf("timeline: %+v", tl)
		}
		if tl.Status == nil || tl.Status.State == "" {
			t.Fatalf("timeline carries no status: %s", stdout)
		}
	}
	// The v1 path still requires the gate: nothing changed for it.
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", id, "--json"); code != 2 {
		t.Fatalf("v1 timeline bypassed the recall gate: exit %d", code)
	}
}

func TestRecallRowsTranscriptFlagAndErrors(t *testing.T) {
	home := t.TempDir()
	fixture, _ := filepath.Abs(filepath.Join("..", "..", "internal", "recall", "query", "testdata", "rows", "codex-rollout.jsonl"))
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", "--rows", "--json", "--transcript", fixture, "--harness", "codex")
	if code != 0 || !strings.Contains(stdout, `"kind": "turn_end"`) {
		t.Fatalf("--transcript: %d %s %s", code, stdout, stderr)
	}
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "--rows", "--json", "--transcript", fixture); code == 0 {
		t.Fatal("--transcript without --harness accepted")
	}
	if _, _, code := runAgentDeck(t, home, "recall", "timeline", "no-such-session", "--rows", "--json"); code == 0 {
		t.Fatal("unknown session accepted")
	}
	stdout, _, _ = runAgentDeck(t, home, "recall", "timeline", "--help")
	_, helpErr, _ := runAgentDeck(t, home, "recall", "timeline", "--help")
	if !strings.Contains(stdout+helpErr, "--rows") {
		t.Fatalf("help does not document --rows: %s %s", stdout, helpErr)
	}
}

// TestRecallRowsFollowCLI: follow from the timeline cursor streams an
// appended line as a row frame within two seconds, and the absorbed queue
// row appears as remove+row under one id.
func TestRecallRowsFollowCLI(t *testing.T) {
	home, id, transcript := rowsTestSession(t)
	stdout, stderr, code := runAgentDeck(t, home, "recall", "timeline", id, "--rows", "--json")
	if code != 0 {
		t.Fatalf("timeline: %d %s %s", code, stdout, stderr)
	}
	var tl rowsTimelineJSON
	if err := json.Unmarshal([]byte(stdout), &tl); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(channelsCLIBinary(t), "recall", "follow", id, "--rows", "--after", tl.ThroughCursor, "--jsonl")
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
	time.Sleep(300 * time.Millisecond)
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
			typ, _ := f["type"].(string)
			if typ == "status" {
				continue
			}
			seen = append(seen, typ)
			if typ == "resync_required" {
				t.Fatalf("resync: %v", f)
			}
			if len(seen) == 1 {
				if lat := time.Since(appended); lat > 2*time.Second {
					t.Fatalf("appended line took %v to stream", lat)
				}
				row, _ := f["row"].(map[string]any)
				if typ != "row" || row["delivery"] != "queued" || f["cursor"] == nil {
					t.Fatalf("queued frame: %v", f)
				}
				appendFileCLI(t, transcript, remove)
			}
			if len(seen) == 3 {
				row, _ := f["row"].(map[string]any)
				if strings.Join(seen, ",") != "row,remove,row" || row["delivery"] != "absorbed" {
					t.Fatalf("absorb frames %v last %v", seen, f)
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
