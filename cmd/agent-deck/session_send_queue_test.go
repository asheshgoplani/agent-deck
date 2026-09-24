package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sendRecordJSON struct {
	SendID       string `json:"send_id"`
	State        string `json:"state"`
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason"`
	TargetStatus string `json:"target_status"`
	SessionID    string `json:"session_id"`
	Message      string `json:"message"`
	Attempts     int    `json:"attempts"`
	Settled      bool   `json:"settled"`
	LandedRowID  string `json:"landed_row_id"`
}

func addSessionJSON(t *testing.T, home, title, tool string) string {
	t.Helper()
	project := filepath.Join(home, "proj-"+title)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", title, "-c", tool, "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add %s: %d %s %s", title, code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add JSON: %v %s", err, stdout)
	}
	return added.ID
}

// TestSessionSendQueueStoppedTargetFailsAtOnce: acceptance (b) — a queued
// send to a target that is not running is failed with that reason at once,
// and send-status reads the same record back.
func TestSessionSendQueueStoppedTargetFailsAtOnce(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "q-stopped", "claude")
	start := time.Now()
	stdout, stderr, code := runAgentDeckStdin(t, home, "hello", "session", "send", id, "--message-file", "-", "--json", "--queue")
	if code != 1 {
		t.Fatalf("queue to a stopped target: exit %d %s %s", code, stdout, stderr)
	}
	var rec sendRecordJSON
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("JSON: %v %s", err, stdout)
	}
	if rec.State != "failed" || rec.Reason != "target not running" || rec.SendID == "" || time.Since(start) > 5*time.Second {
		t.Fatalf("record: %+v after %v", rec, time.Since(start))
	}
	stdout, _, code = runAgentDeck(t, home, "session", "send-status", rec.SendID, "--json")
	var again sendRecordJSON
	if code != 0 || json.Unmarshal([]byte(stdout), &again) != nil || again.State != "failed" || again.SessionID != id {
		t.Fatalf("send-status: %d %s", code, stdout)
	}
	if _, _, code := runAgentDeck(t, home, "session", "send-status", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json"); code != 2 {
		t.Fatalf("unknown send id: exit %d, want 2", code)
	}
	if _, _, code := runAgentDeck(t, home, "session", "send", id, "hi", "--queue", "--wait"); code != 2 {
		t.Fatalf("--queue --wait accepted: exit %d", code)
	}
}

// TestSessionSendImages: acceptance (c)/(d) at the CLI boundary — Claude gets
// @<copy under .agentdeck-images/>, Codex and non-images exit 2.
func TestSessionSendImages(t *testing.T) {
	home := t.TempDir()
	png := filepath.Join(home, "shot.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := addSessionJSON(t, home, "img-codex", "codex")
	stdout, stderr, code := runAgentDeck(t, home, "session", "send", codex, "look", "--image", png, "--json")
	if code != 2 || !strings.Contains(stdout+stderr, "images not supported for codex") {
		t.Fatalf("codex image: exit %d %s %s", code, stdout, stderr)
	}
	claude := addSessionJSON(t, home, "img-claude", "claude")
	txt := filepath.Join(home, "notes.txt")
	_ = os.WriteFile(txt, []byte("x"), 0o644)
	if _, _, code := runAgentDeck(t, home, "session", "send", claude, "look", "--image", txt); code != 2 {
		t.Fatalf("non-image accepted: exit %d", code)
	}
	// Queued to a stopped Claude session: the record keeps the @path message
	// and the copy exists next to the session.
	stdout, _, _ = runAgentDeck(t, home, "session", "send", claude, "what is this?", "--image", png, "--json", "--queue")
	var rec sendRecordJSON
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("JSON: %v %s", err, stdout)
	}
	i := strings.Index(rec.Message, "@")
	if !strings.HasPrefix(rec.Message, "what is this? @") || i < 0 {
		t.Fatalf("message: %q", rec.Message)
	}
	copyPath := rec.Message[i+1:]
	if !strings.Contains(copyPath, string(filepath.Separator)+".agentdeck-images"+string(filepath.Separator)) {
		t.Fatalf("image copy path: %q", copyPath)
	}
	if data, err := os.ReadFile(copyPath); err != nil || string(data) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("image copy: %v", err)
	}
	stdout, _, _ = runAgentDeck(t, home, "session", "send", "--help")
	for _, want := range []string{"--queue", "--image", "send-status"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("send --help lacks %s", want)
		}
	}
}

// TestSessionSendQueueDeliversThroughWorker drives the detached worker end
// to end on a real tmux shell: the queued text is typed and verified by the
// normal send path; a shell has no transcript, so the record settles
// without a landed row instead of being typed twice.
func TestSessionSendQueueDeliversThroughWorker(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	extra := []string{"AGENTDECK_SEND_WORKER_POLL=200ms", "AGENTDECK_SEND_LAND_WINDOW=2s"}
	if d := os.Getenv("TMUX_TMPDIR"); d != "" {
		extra = append(extra, "TMUX_TMPDIR="+d)
	}
	run := func(stdin string, args ...string) (string, string, int) {
		return runAgentDeckEnv(t, home, stdin, extra, args...)
	}
	project := filepath.Join(home, "sh")
	_ = os.MkdirAll(project, 0o755)
	stdout, stderr, code := run("", "add", "-t", "q-shell", "-c", "shell", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(stdout), &added)
	if stdout, stderr, code := run("", "session", "start", added.ID, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = run("", "session", "stop", added.ID) })
	time.Sleep(time.Second)
	start := time.Now()
	stdout, stderr, code = run("echo queued-worker-ok", "session", "send", added.ID, "--message-file", "-", "--json", "--queue")
	if code != 0 {
		t.Fatalf("queue: %d %s %s", code, stdout, stderr)
	}
	var rec sendRecordJSON
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil || rec.State != "queued" || rec.SendID == "" {
		t.Fatalf("queued record: %v %s", err, stdout)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("--queue took %v, want <1s", elapsed)
	} else {
		t.Logf("explicit queue return: %d ms", elapsed.Milliseconds())
	}
	if rec.Verdict != "queued" {
		t.Fatalf("initial verdict = %q, want queued", rec.Verdict)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		stdout, _, _ = run("", "session", "send-status", rec.SendID, "--json")
		var st sendRecordJSON
		_ = json.Unmarshal([]byte(stdout), &st)
		if st.State == "failed" {
			t.Fatalf("worker failed the send: %+v", st)
		}
		if st.Settled || st.State == "landed" {
			if st.Attempts != 1 || (st.State != "typed" && st.State != "submitted") {
				t.Fatalf("settled record: %+v", st)
			}
			start := time.Now()
			stdout, stderr, code = run("echo json-fast-ok", "session", "send", added.ID, "--message-file", "-", "--json")
			var fast sendRecordJSON
			if code != 0 || json.Unmarshal([]byte(stdout), &fast) != nil || fast.SendID == "" || fast.Verdict != "queued" {
				t.Fatalf("plain --json send: %d %s %s", code, stdout, stderr)
			}
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("plain --json send took %v, want <1s", elapsed)
			} else {
				t.Logf("plain JSON return: %d ms", elapsed.Milliseconds())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("send never delivered: %s", stdout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
