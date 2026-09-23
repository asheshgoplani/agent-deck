package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRollout writes a minimal Codex rollout: a session_meta head and, when
// extra is set, one more line. A parent makes it a sub-agent thread.
func writeRollout(t *testing.T, home, id, cwd, parent, extra string, mtime time.Time) string {
	t.Helper()
	src := `"cli"`
	if parent != "" {
		src = fmt.Sprintf(`{"subagent":{"thread_spawn":{"parent_thread_id":%q}}}`, parent)
	}
	return writeRolloutSource(t, home, id, cwd, parent, src, extra, mtime)
}

// writeRolloutSource is writeRollout with an explicit session_meta source
// (`"cli"`, `"exec"`, a sub-agent object).
func writeRolloutSource(t *testing.T, home, id, cwd, parent, src, extra string, mtime time.Time) string {
	t.Helper()
	day := filepath.Join(home, "sessions", mtime.Format("2006"), mtime.Format("01"), mtime.Format("02"))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	parentField := ""
	threadSource := "user"
	if parent != "" {
		threadSource = "subagent"
		parentField = fmt.Sprintf(`,"parent_thread_id":%q`, parent)
	}
	body := fmt.Sprintf(`{"timestamp":"2026-09-23T07:59:36.742Z","type":"session_meta","payload":{"session_id":%q,"id":%q,"cwd":%q,"source":%s,"thread_source":%q%s}}`+"\n", id, id, cwd, src, threadSource, parentField)
	if extra != "" {
		body += extra + "\n"
	}
	path := filepath.Join(day, "rollout-2026-09-23T09-58-25-"+id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// noPaneProcess stubs the pane process probe: these instances have no pane.
func noPaneProcess(t *testing.T) {
	t.Helper()
	stubCodexPaneOpenPaths(t, nil, errors.New("no pane"))
}

// TestCodexLiveRolloutMacappReceiptCase reproduces the Mac app receipt:
// session show said codex_session_id 01a0cd46 while the rollouts on disk
// were 01a0cd45 (the conversation) and 01a0cd47 (its Explore sub-agent).
func TestCodexLiveRolloutMacappReceiptCase(t *testing.T) {
	noPaneProcess(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	main := writeRollout(t, home, "01a0cd45-e811-7dd2-825d-f110cd204fb0", cwd, "", "", now.Add(-2*time.Second))
	sub := writeRollout(t, home, "01a0cd47-7719-7df3-8eb3-1616c67ce9ea", cwd, "01a0cd45-e811-7dd2-825d-f110cd204fb0", "", now)
	writeRollout(t, home, "01a0cd99-0000-7000-8000-000000000000", t.TempDir(), "", "", now) // other directory
	inst := &Instance{ID: "c1", Tool: "codex", ProjectPath: cwd, CodexSessionID: "01a0cd46-fefc-7590-aa92-c26773921d1d", CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(inst, nil); got != main {
		t.Fatalf("live rollout = %s, want the user thread %s (not the sub-agent %s)", got, main, sub)
	}
	if got := LiveTranscriptPath(inst, nil); got != main {
		t.Fatalf("LiveTranscriptPath = %s", got)
	}
	ids := TranscriptIDs(inst, nil)
	if len(ids) != 2 || ids[0] != "01a0cd45-e811-7dd2-825d-f110cd204fb0" || ids[1] != "01a0cd46-fefc-7590-aa92-c26773921d1d" {
		t.Fatalf("transcript ids = %v", ids)
	}
	// The exact rollout of a stored user-thread id wins.
	inst.CodexSessionID = "01a0cd45-e811-7dd2-825d-f110cd204fb0"
	if got := CodexLiveRolloutPath(inst, nil); got != main {
		t.Fatalf("exact id: %s", got)
	}
	// A stored id that names the sub-agent thread is not the conversation.
	inst.CodexSessionID = "01a0cd47-7719-7df3-8eb3-1616c67ce9ea"
	if got := CodexLiveRolloutPath(inst, nil); got != main {
		t.Fatalf("sub-agent id: %s", got)
	}
}

func TestCodexLiveRolloutPrefersTheOneReferencingTheStoredID(t *testing.T) {
	noPaneProcess(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	stored := "01b00000-0000-7000-8000-000000000001"
	refs := writeRollout(t, home, "01b00000-0000-7000-8000-00000000000a", cwd, "", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"`+stored+`"}}`, now.Add(-5*time.Second))
	writeRollout(t, home, "01b00000-0000-7000-8000-00000000000b", cwd, "", "", now)
	inst := &Instance{ID: "c2", Tool: "codex", ProjectPath: cwd, CodexSessionID: stored, CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(inst, nil); got != refs {
		t.Fatalf("got %s, want the rollout referencing the stored id", got)
	}
	// Without a reference two user threads in the directory are ambiguous:
	// never guess the newest.
	inst.CodexSessionID = ""
	if got := CodexLiveRolloutPath(inst, nil); got != "" {
		t.Fatalf("no stored id, two candidates: got %s, want none", got)
	}
	// Rollouts older than the session never qualify.
	inst.CreatedAt = now.Add(time.Hour)
	if got := CodexLiveRolloutPath(inst, nil); got != "" {
		t.Fatalf("rollout older than the session chosen: %s", got)
	}
	// Remote sessions never resolve a local rollout.
	remote := &Instance{ID: "c3", Tool: "codex", ProjectPath: cwd, SSHHost: "box", CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(remote, nil); got != "" {
		t.Fatalf("remote session resolved %s", got)
	}
}

// TestCodexLiveRolloutNeverBindsAnotherThread: the review's wrong-thread
// cases. With the stored id's rollout missing, session A must not follow a
// thread bound to deck session B, a `codex exec` run, a sub-agent, or a
// rollout that merely quotes A's id in chat.
func TestCodexLiveRolloutNeverBindsAnotherThread(t *testing.T) {
	noPaneProcess(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	stored := "01c00000-0000-7000-8000-000000000001"
	mine := writeRollout(t, home, "01c00000-0000-7000-8000-00000000000a", cwd, "", "", now.Add(-10*time.Second))
	peerThread := "01c00000-0000-7000-8000-00000000000b"
	writeRollout(t, home, peerThread, cwd, "", "", now.Add(-time.Second))
	writeRolloutSource(t, home, "01c00000-0000-7000-8000-00000000000c", cwd, "", `"exec"`, "", now)
	writeRollout(t, home, "01c00000-0000-7000-8000-00000000000d", cwd, "01c00000-0000-7000-8000-00000000000a", "", now)
	quoting := writeRollout(t, home, "01c00000-0000-7000-8000-00000000000e", cwd, "",
		`{"type":"event_msg","payload":{"type":"user_message","message":"what is thread `+stored+`?"}}`, now)
	a := &Instance{ID: "a", Tool: "codex", ProjectPath: cwd, CodexSessionID: stored, CreatedAt: now.Add(-time.Minute)}
	b := &Instance{ID: "b", Tool: "codex", ProjectPath: cwd, CodexSessionID: peerThread, CreatedAt: now.Add(-time.Minute)}
	// mine and quoting are both unowned user threads and neither references
	// the stored id structurally: ambiguous, so nothing.
	if got := CodexLiveRolloutPath(a, []*Instance{a, b}); got != "" {
		t.Fatalf("ambiguous candidates resolved to %s", got)
	}
	if err := os.Remove(quoting); err != nil {
		t.Fatal(err)
	}
	if got := CodexLiveRolloutPath(a, []*Instance{a, b}); got != mine {
		t.Fatalf("got %s, want the only unowned user thread %s", got, mine)
	}
	if ids := TranscriptIDs(a, []*Instance{a, b}); len(ids) != 2 || ids[0] != "01c00000-0000-7000-8000-00000000000a" || ids[1] != stored {
		t.Fatalf("transcript ids = %v (another thread listed)", ids)
	}
	// Without the peer list B's thread is a second candidate: ambiguous.
	if got := CodexLiveRolloutPath(a, nil); got != "" {
		t.Fatalf("without peers: %s", got)
	}
}

// TestCodexLiveRolloutPrefersThePaneProcessThread: the thread the pane's own
// Codex process holds open is the conversation, whatever else is on disk.
func TestCodexLiveRolloutPrefersThePaneProcessThread(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	held := writeRollout(t, home, "01d00000-0000-7000-8000-00000000000a", cwd, "", "", now.Add(-10*time.Second))
	writeRollout(t, home, "01d00000-0000-7000-8000-00000000000b", cwd, "", "", now)
	stubCodexPaneOpenPaths(t, []string{held}, nil)
	inst := &Instance{ID: "p", Tool: "codex", ProjectPath: cwd, CodexSessionID: "01d00000-0000-7000-8000-000000000001", CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(inst, nil); got != held {
		t.Fatalf("got %s, want the pane process's thread %s", got, held)
	}
	// A held thread bound to another session is not this one's.
	peer := &Instance{ID: "q", Tool: "codex", ProjectPath: cwd, CodexSessionID: "01d00000-0000-7000-8000-00000000000a"}
	if got := CodexLiveRolloutPath(inst, []*Instance{inst, peer}); got == held {
		t.Fatalf("followed a thread bound to another session: %s", got)
	}
}
