package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRollout writes a minimal Codex rollout: a session_meta head and, when
// extra is set, one more line.
func writeRollout(t *testing.T, home, id, cwd, parent, extra string, mtime time.Time) string {
	t.Helper()
	day := filepath.Join(home, "sessions", mtime.Format("2006"), mtime.Format("01"), mtime.Format("02"))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	src, parentField := `"cli"`, ""
	threadSource := "user"
	if parent != "" {
		threadSource = "subagent"
		parentField = fmt.Sprintf(`,"parent_thread_id":%q`, parent)
		src = fmt.Sprintf(`{"subagent":{"thread_spawn":{"parent_thread_id":%q}}}`, parent)
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

// TestCodexLiveRolloutMacappReceiptCase reproduces the Mac app receipt:
// session show said codex_session_id 01a0cd46 while the rollouts on disk
// were 01a0cd45 (the conversation) and 01a0cd47 (its Explore sub-agent).
func TestCodexLiveRolloutMacappReceiptCase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	main := writeRollout(t, home, "01a0cd45-e811-7dd2-825d-f110cd204fb0", cwd, "", "", now.Add(-2*time.Second))
	sub := writeRollout(t, home, "01a0cd47-7719-7df3-8eb3-1616c67ce9ea", cwd, "01a0cd45-e811-7dd2-825d-f110cd204fb0", "", now)
	writeRollout(t, home, "01a0cd99-0000-7000-8000-000000000000", t.TempDir(), "", "", now) // other directory
	inst := &Instance{ID: "c1", Tool: "codex", ProjectPath: cwd, CodexSessionID: "01a0cd46-fefc-7590-aa92-c26773921d1d", CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(inst); got != main {
		t.Fatalf("live rollout = %s, want the user thread %s (not the sub-agent %s)", got, main, sub)
	}
	if got := LiveTranscriptPath(inst); got != main {
		t.Fatalf("LiveTranscriptPath = %s", got)
	}
	ids := TranscriptIDs(inst)
	if len(ids) != 2 || ids[0] != "01a0cd45-e811-7dd2-825d-f110cd204fb0" || ids[1] != "01a0cd46-fefc-7590-aa92-c26773921d1d" {
		t.Fatalf("transcript ids = %v", ids)
	}
	// The exact rollout of a stored user-thread id wins.
	inst.CodexSessionID = "01a0cd45-e811-7dd2-825d-f110cd204fb0"
	if got := CodexLiveRolloutPath(inst); got != main {
		t.Fatalf("exact id: %s", got)
	}
	// A stored id that names the sub-agent thread is not the conversation.
	inst.CodexSessionID = "01a0cd47-7719-7df3-8eb3-1616c67ce9ea"
	if got := CodexLiveRolloutPath(inst); got != main {
		t.Fatalf("sub-agent id: %s", got)
	}
}

func TestCodexLiveRolloutPrefersTheOneMentioningTheStoredID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cwd := t.TempDir()
	now := time.Now()
	stored := "01b00000-0000-7000-8000-000000000001"
	mentions := writeRollout(t, home, "01b00000-0000-7000-8000-00000000000a", cwd, "", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"`+stored+`"}}`, now.Add(-5*time.Second))
	newer := writeRollout(t, home, "01b00000-0000-7000-8000-00000000000b", cwd, "", "", now)
	inst := &Instance{ID: "c2", Tool: "codex", ProjectPath: cwd, CodexSessionID: stored, CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(inst); got != mentions {
		t.Fatalf("got %s, want the rollout mentioning the stored id", got)
	}
	inst.CodexSessionID = ""
	if got := CodexLiveRolloutPath(inst); got != newer {
		t.Fatalf("no stored id: got %s, want newest %s", got, newer)
	}
	// Rollouts older than the session never qualify.
	inst.CreatedAt = now.Add(time.Hour)
	if got := CodexLiveRolloutPath(inst); got != "" {
		t.Fatalf("rollout older than the session chosen: %s", got)
	}
	// Remote sessions never resolve a local rollout.
	remote := &Instance{ID: "c3", Tool: "codex", ProjectPath: cwd, SSHHost: "box", CreatedAt: now.Add(-time.Minute)}
	if got := CodexLiveRolloutPath(remote); got != "" {
		t.Fatalf("remote session resolved %s", got)
	}
}
