package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2481 item 5: send records in the health journal carried no sender
// and no text, a queued send refused with composer_blocked was retried every
// second (19 and 15 attempts within five minutes measured), and a queued
// send that failed was never reported back to its sender.

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

// keyedTextHash recomputes a record's text_hmac with the profile key the
// binary under home created.
func keyedTextHash(t *testing.T, home, s string) string {
	t.Helper()
	var key []byte
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "send-text.key" {
			key, _ = os.ReadFile(path)
		}
		return nil
	})
	if len(key) != 32 {
		t.Fatalf("no 32-byte send text key under the test home (got %d bytes)", len(key))
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// TestSendJournalRecordCarriesSenderAndTextHash: a send journals who sent it
// (the calling session's id, or "cli" from a plain shell), a keyed hash of
// the text (never the text) and its length, next to the existing outcome.
// A queued send journals its delivery attempt (queuer as sender, send_id,
// attempt) and one final record with its terminal state; a quiet queued
// send still says on stderr that it is not delivered yet.
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
	stdout, stderr, code = run([]string{"AGENTDECK_INSTANCE_ID=queuer-xyz-789"}, queued, "session", "send", added.ID, "--message-file", "-", "--queue", "-q")
	if code != 0 {
		t.Fatalf("queue: %d %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "not delivered yet") {
		t.Errorf("quiet queued send printed no notice on stderr: stdout %q stderr %q", stdout, stderr)
	}

	// Direct sends: one record each. Queued send: one attempt record and one
	// final record.
	deadline := time.Now().Add(60 * time.Second)
	var sends []map[string]any
	for {
		sends = readJournalSends(t, home)
		if len(sends) >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if len(sends) != 4 {
		t.Fatalf("journaled %d sends, want 4: %v", len(sends), sends)
	}
	type want struct {
		sender, text string
		queued       bool
	}
	byHash := map[string]want{}
	for _, w := range []want{{"sender-abc-123", fromSession, false}, {"cli", fromCLI, false}, {"queuer-xyz-789", queued, true}} {
		byHash[keyedTextHash(t, home, w.text)] = w
	}
	var attempts, finals int
	var queuedSendID string
	for _, ev := range sends {
		detail, _ := ev["detail"].(map[string]any)
		raw, _ := json.Marshal(ev)
		for _, text := range []string{fromSession, fromCLI, queued} {
			if strings.Contains(string(raw), text) {
				t.Fatalf("journal holds the raw text %q: %s", text, raw)
			}
		}
		hash, _ := detail["text_hmac"].(string)
		w, ok := byHash[hash]
		if !ok {
			t.Fatalf("send record has no known text_hmac: %s", raw)
		}
		if detail["sender"] != w.sender {
			t.Errorf("sender = %v, want %q: %s", detail["sender"], w.sender, raw)
		}
		if n, _ := detail["text_len"].(float64); int(n) != len(w.text) {
			t.Errorf("text_len = %v, want %d: %s", detail["text_len"], len(w.text), raw)
		}
		if detail["outcome"] == nil || detail["delivery"] == nil {
			t.Errorf("outcome/delivery missing: %s", raw)
		}
		if !w.queued {
			continue
		}
		id, _ := detail["send_id"].(string)
		if id == "" || (queuedSendID != "" && id != queuedSendID) {
			t.Errorf("queued record send_id = %q: %s", id, raw)
		}
		queuedSendID = id
		if detail["final"] == true {
			finals++
			if n, _ := detail["attempts"].(float64); n != 1 {
				t.Errorf("final record attempts = %v, want 1: %s", detail["attempts"], raw)
			}
		} else {
			attempts++
			if n, _ := detail["attempt"].(float64); n != 1 {
				t.Errorf("queued attempt = %v, want 1: %s", detail["attempt"], raw)
			}
		}
	}
	if attempts != 1 || finals != 1 {
		t.Fatalf("queued send journaled %d attempt and %d final records, want 1 and 1", attempts, finals)
	}
}

// retryFixture is a running shell target saved in an isolated profile, plus
// a sender session the profile knows, for driving deliverQueued in-process.
type retryFixture struct {
	profile, dir string
	target       *session.Instance
	senderID     string
}

func newRetryFixture(t *testing.T, profile string) retryFixture {
	t.Helper()
	skipIfNoTmuxBinaryCLI(t)
	target := session.NewInstanceWithTool("retry-target", t.TempDir(), "shell")
	target.Status = session.StatusRunning
	target.GroupPath = session.DefaultGroupPath
	sess := target.GetTmuxSession()
	if err := sess.Start("bash"); err != nil {
		t.Fatalf("start fixture tmux session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	sender := session.NewInstanceWithTool("retry-sender", t.TempDir(), "claude")
	sender.GroupPath = session.DefaultGroupPath
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionData(storage, []*session.Instance{target, sender}, nil); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	return retryFixture{profile: profile, dir: t.TempDir(), target: target, senderID: sender.ID}
}

// queue saves a queued record for the target. The sender is written into
// the JSON directly so this file builds against a Record without the field.
func (f retryFixture) queue(t *testing.T, budget time.Duration, sender string) *sendqueue.Record {
	t.Helper()
	now := time.Now()
	rec := &sendqueue.Record{SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, Verdict: "queued", SessionID: f.target.ID, SessionTitle: f.target.Title, Tool: "shell", Message: "echo blocked",
		CreatedAt: now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano), Deadline: now.Add(budget).UTC().Format(time.RFC3339Nano)}
	b, _ := json.Marshal(rec)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["sender"] = sender
	b, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(f.dir, rec.SendID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// stubChild makes every delivery child report composer_blocked for its
// first refusals calls, then a confirmed submit; it records start times.
func stubChild(t *testing.T, refusals int) func() []time.Time {
	t.Helper()
	var mu sync.Mutex
	var starts []time.Time
	prev := sendChild
	sendChild = func(profile, id, message, resultPath string) (int, func() int, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		n := len(starts)
		mu.Unlock()
		result, code := `{"success":false,"delivery":"composer_blocked","error":"composer not safe"}`, 1
		if refusals >= 0 && n > refusals {
			result, code = `{"success":true,"submitted":true,"delivery":"submitted"}`, 0
		}
		_ = os.WriteFile(resultPath, []byte(result), 0o600)
		return 4242, func() int { return code }, nil
	}
	t.Cleanup(func() { sendChild = prev })
	return func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), starts...)
	}
}

// TestQueuedSendRetriesBackOffAndStillDeliver: a composer that stays
// blocked for a while (a human typing into it) is retried with a growing
// wait, not every poll, and the send still goes through once it clears:
// no attempt count turns a send that would land into a failure.
func TestQueuedSendRetriesBackOffAndStillDeliver(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	f := newRetryFixture(t, "_test_send_retry_backoff")
	starts := stubChild(t, 6)
	rec := f.queue(t, time.Hour, "cli")

	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("still delivering after %d attempts", len(starts()))
	}
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State == sendqueue.StateFailed || got.Attempts != 7 {
		t.Fatalf("record = state %q attempts %d reason %q; want delivered on attempt 7", got.State, got.Attempts, got.Reason)
	}
	s := starts()
	// The gaps between refused attempts double from the poll: the last gap
	// is several times the first, where a fixed poll keeps them equal.
	first, last := s[1].Sub(s[0]), s[len(s)-1].Sub(s[len(s)-2])
	if last < 4*first || last < 1200*time.Millisecond {
		t.Fatalf("retry gaps did not back off: first %v, last %v (all starts %v)", first, last, s)
	}
}

// TestQueuedSendFailureReachesTheSender: a send refused for its whole budget
// fails only when the budget is spent, at a bounded attempt rate, journals
// one final record and puts a notice in the sender session's inbox.
func TestQueuedSendFailureReachesTheSender(t *testing.T) {
	t.Cleanup(session.SetCommsLedgerForTest(true))
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "20ms")
	f := newRetryFixture(t, "_test_send_retry_notice")
	starts := stubChild(t, -1)
	rec := f.queue(t, 1500*time.Millisecond, f.senderID)

	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("still retrying after %d attempts", len(starts()))
	}
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sendqueue.StateFailed || !strings.Contains(got.Reason, "composer_blocked") {
		t.Fatalf("record = state %q reason %q, want failed with the composer_blocked reason", got.State, got.Reason)
	}
	// 1.5 s at a 20 ms poll: doubling allows about 7 attempts plus one at
	// the end of the budget; a fixed poll makes dozens.
	if n := len(starts()); n > 10 {
		t.Fatalf("%d attempts in a 1.5 s budget; want the backoff to bound the rate", n)
	}

	inbox, err := session.ReadInboxEvents(f.senderID)
	if err != nil {
		t.Fatal(err)
	}
	var notice *session.TransitionNotificationEvent
	for i := range inbox {
		if inbox[i].ToStatus == "send_failed" && strings.Contains(inbox[i].Text, rec.SendID) {
			notice = &inbox[i]
		}
	}
	if notice == nil {
		t.Fatalf("sender inbox has no send_failed notice for %s: %+v", rec.SendID, inbox)
	}
	spool, err := session.ReadCommsSpool(rec.SessionID)
	if err != nil || len(spool) != 1 || spool[0].Event != "async-inbox" || spool[0].From != f.senderID {
		t.Fatalf("ledger must reuse the committed inbox failure: %+v, %v", spool, err)
	}

	dir, err := session.HealthLogDir(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := health.ReadEvents(dir, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	finals := 0
	for _, e := range events {
		if e.Kind == health.KindSend && e.Detail["final"] == true && e.Detail["send_id"] == rec.SendID {
			finals++
			if e.Detail["outcome"] != health.SendFailed || e.Detail["sender"] != f.senderID {
				t.Errorf("final record = %+v", e.Detail)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("%d final journal records for %s, want 1", finals, rec.SendID)
	}
}
