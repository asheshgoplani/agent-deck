package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2481 item 5: send records in the health journal carried no sender
// and no text, and a queued send refused with composer_blocked was retyped
// until its 30 minute budget ran out (14 and 18 loops measured).

// readJournalSends returns every send event the binary under home journaled.
func readJournalSends(t *testing.T, home string) []map[string]any {
	t.Helper()
	var sends []map[string]any
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasPrefix(info.Name(), "sessions-") || filepath.Base(filepath.Dir(path)) != "health" {
			return nil
		}
		b, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(b), "\n") {
			var ev map[string]any
			if json.Unmarshal([]byte(line), &ev) == nil && ev["kind"] == "send" {
				sends = append(sends, ev)
			}
		}
		return nil
	})
	return sends
}

func textHashPrefix(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// TestSendJournalRecordCarriesSenderAndTextHash: a send journals who sent it
// (the calling session's id, or "cli" from a plain shell), a sha256 prefix
// of the text (never the text) and its length, next to the existing outcome.
// A queued send's delivery attempt carries the queuer as sender, its send_id
// and the attempt number.
func TestSendJournalRecordCarriesSenderAndTextHash(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	extra := []string{"AGENTDECK_SEND_WORKER_POLL=200ms", "AGENTDECK_SEND_LAND_WINDOW=1s"}
	if d := os.Getenv("TMUX_TMPDIR"); d != "" {
		extra = append(extra, "TMUX_TMPDIR="+d)
	}
	run := func(env []string, stdin string, args ...string) (string, string, int) {
		return runAgentDeckEnv(t, home, stdin, append(append([]string{}, extra...), env...), args...)
	}
	project := filepath.Join(home, "sh")
	_ = os.MkdirAll(project, 0o755)
	stdout, stderr, code := run(nil, "", "add", "-t", "rec-shell", "-c", "shell", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(stdout), &added)
	if stdout, stderr, code := run(nil, "", "session", "start", added.ID, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = run(nil, "", "session", "stop", added.ID) })
	time.Sleep(time.Second)

	const fromSession, fromCLI, queued = "echo from-session-ok", "echo from-cli-ok", "echo from-queue-ok"
	if stdout, stderr, code := run([]string{"AGENTDECK_INSTANCE_ID=sender-abc-123"}, "", "session", "send", added.ID, fromSession, "--no-wait", "--json"); code != 0 {
		t.Fatalf("send from session: %d %s %s", code, stdout, stderr)
	}
	if stdout, stderr, code := run(nil, "", "session", "send", added.ID, fromCLI, "--no-wait", "--json"); code != 0 {
		t.Fatalf("send from cli: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = run([]string{"AGENTDECK_INSTANCE_ID=queuer-xyz-789"}, queued, "session", "send", added.ID, "--message-file", "-", "--json", "--queue")
	if code != 0 {
		t.Fatalf("queue: %d %s %s", code, stdout, stderr)
	}
	var rec sendRecordJSON
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil || rec.SendID == "" {
		t.Fatalf("queued record: %v %s", err, stdout)
	}

	want := map[string]struct{ sender, text, sendID string }{
		textHashPrefix(fromSession): {"sender-abc-123", fromSession, ""},
		textHashPrefix(fromCLI):     {"cli", fromCLI, ""},
		textHashPrefix(queued):      {"queuer-xyz-789", queued, rec.SendID},
	}
	deadline := time.Now().Add(60 * time.Second)
	var sends []map[string]any
	for {
		sends = readJournalSends(t, home)
		if len(sends) >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if len(sends) != 3 {
		t.Fatalf("journaled %d sends, want 3: %v", len(sends), sends)
	}
	for _, ev := range sends {
		detail, _ := ev["detail"].(map[string]any)
		raw, _ := json.Marshal(ev)
		for _, text := range []string{fromSession, fromCLI, queued} {
			if strings.Contains(string(raw), text) {
				t.Fatalf("journal holds the raw text %q: %s", text, raw)
			}
		}
		hash, _ := detail["text_sha256"].(string)
		w, ok := want[hash]
		if !ok {
			t.Fatalf("send record has no known text_sha256: %s", raw)
		}
		delete(want, hash)
		if detail["sender"] != w.sender {
			t.Errorf("sender = %v, want %q: %s", detail["sender"], w.sender, raw)
		}
		if n, _ := detail["text_len"].(float64); int(n) != len(w.text) {
			t.Errorf("text_len = %v, want %d: %s", detail["text_len"], len(w.text), raw)
		}
		if detail["outcome"] == nil || detail["delivery"] == nil {
			t.Errorf("outcome/delivery missing: %s", raw)
		}
		if w.sendID != "" {
			if detail["send_id"] != w.sendID {
				t.Errorf("queued attempt send_id = %v, want %s: %s", detail["send_id"], w.sendID, raw)
			}
			if n, _ := detail["attempt"].(float64); n != 1 {
				t.Errorf("queued attempt = %v, want 1: %s", detail["attempt"], raw)
			}
		}
	}
}

// TestQueuedSendRetriesAreBounded: a target whose composer keeps refusing
// the send (composer_blocked: nothing typed) is retried a bounded number of
// times, then the record fails with the refusal as its reason — the state
// `send-status` reports to the sender — instead of looping until the 30
// minute retry budget runs out.
func TestQueuedSendRetriesAreBounded(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "10ms")
	const maxAttempts = 5 // the documented default bound

	profile := "_test_send_retry_bound"
	inst := session.NewInstanceWithTool("retry-bound-target", t.TempDir(), "shell")
	inst.Status = session.StatusRunning
	inst.GroupPath = session.DefaultGroupPath
	sess := inst.GetTmuxSession()
	if err := sess.Start("bash"); err != nil {
		t.Fatalf("start fixture tmux session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionData(storage, []*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()

	dir := t.TempDir()
	now := time.Now()
	rec := &sendqueue.Record{SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, Verdict: "queued", SessionID: inst.ID, Tool: "shell", Message: "echo blocked",
		CreatedAt: now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano), Deadline: now.Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}

	var calls, stop atomic.Int32
	prev := sendChild
	sendChild = func(profile, id, message, resultPath string) (int, func() int, error) {
		calls.Add(1)
		if stop.Load() == 0 {
			_ = os.WriteFile(resultPath, []byte(`{"success":false,"delivery":"composer_blocked","error":"composer not safe"}`), 0o600)
		}
		return 4242, func() int { return 1 }, nil
	}
	done := make(chan struct{})
	go func() { deliverQueued(profile, dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		n := calls.Load()
		// Let the leaked worker settle on its own: the next attempt "lands"
		// as an unknown outcome and the loop exits.
		stop.Store(1)
		<-done
		sendChild = prev
		t.Fatalf("queued send still retrying after %d attempts; want failed after %d", n, maxAttempts)
	}
	sendChild = prev

	got, err := sendqueue.Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sendqueue.StateFailed || !strings.Contains(got.Reason, "composer_blocked") {
		t.Fatalf("record = state %q reason %q, want failed with the composer_blocked reason", got.State, got.Reason)
	}
	if got.Attempts != maxAttempts || int(calls.Load()) != maxAttempts {
		t.Fatalf("attempts = %d (child starts %d), want %d", got.Attempts, calls.Load(), maxAttempts)
	}
}
