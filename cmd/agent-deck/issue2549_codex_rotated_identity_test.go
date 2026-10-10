package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2549: after Codex re-authed or restarted inside the pane, the live
// process owned a new thread while agent-deck kept the old registered
// CodexSessionID. Every send was then refused (acceptance_refused) because the
// send path only hydrated an EMPTY identity from the live process, and the
// send queue retried that refusal for its whole 30 minute budget.

const (
	issue2549OldThread = "01a0cc56-1111-7531-9a25-c53c0d193753"
	issue2549NewThread = "01a0cc56-2222-7531-9a25-c53c0d193753"
	issue2549Other     = "01a0cc56-3333-7531-9a25-c53c0d193753"
)

// startRotatedCodexPane starts a fake Codex in the instance's pane that
// holds the writer lock of every thread in held (the way Codex 0.155+ owns a
// thread) and waits until the process probe sees them.
func startRotatedCodexPane(t *testing.T, root, home string, inst *session.Instance, paneEnvID string, held ...string) {
	t.Helper()
	locks := filepath.Join(home, "thread-writer-locks")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{locks, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fakeCodex := filepath.Join(bin, "codex")
	script := "#!/bin/sh\nfd=3\nfor lock in \"$@\"; do eval \"exec $fd>>\\\"\\$lock\\\"\"; fd=$((fd+1)); done\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{fakeCodex}
	for _, id := range held {
		args = append(args, filepath.Join(locks, id+".lock"))
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start(strings.Join(args, " ")); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	if paneEnvID != "" {
		if err := tmuxSess.SetEnvironment("CODEX_SESSION_ID", paneEnvID); err != nil {
			t.Fatalf("set pane identity: %v", err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		paths, _ := os.ReadDir(locks)
		ready := len(held) > 0 && len(paths) >= len(held)
		if ready && (len(held) > 1 || inst.LiveCodexThreadID() == held[0]) {
			// Two held threads make the probe ambiguous by design; give
			// the second descriptor a moment to open.
			if len(held) > 1 {
				time.Sleep(500 * time.Millisecond)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane process never held its writer locks (live thread %q)", inst.LiveCodexThreadID())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func newIssue2549Instance(t *testing.T, root, profile, title string) (*session.Instance, *session.Storage) {
	t.Helper()
	project := filepath.Join(root, "project-"+title)
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool(title, project, "codex")
	inst.Status = session.StatusWaiting
	inst.CodexSessionID = issue2549OldThread
	inst.CodexDetectedAt = time.Now().Add(-time.Hour)
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return inst, storage
}

func issue2549Guard(inst *session.Instance, peers []*session.Instance, storage *session.Storage) (*codexAcceptanceGuard, error) {
	if err := hydrateLegacyCodexIdentity(inst, peers, storage); err != nil {
		return nil, err
	}
	return acquireCodexAcceptanceGuard(inst, time.Second)
}

// The live process moved to a new thread and the registered id has no
// rollout under this Codex home: the exact symptom in the report ("current
// rollout generation is unavailable"). The send must adopt the one thread the
// live process owns, persist it and point the pane identity at it.
func TestIssue2549_SendAdoptsRotatedLiveThread(t *testing.T) {
	for _, oldHasRollout := range []bool{false, true} {
		name := "old id without rollout"
		if oldHasRollout {
			name = "old id with a stale rollout"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "codex")
			t.Setenv("CODEX_HOME", home)
			if oldHasRollout {
				writeLegacyCodexRollout(t, home, issue2549OldThread, "21")
			}
			inst, storage := newIssue2549Instance(t, root, "issue2549_rotated", "rotated-codex")
			if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
				t.Fatal(err)
			}
			startRotatedCodexPane(t, root, home, inst, issue2549OldThread, issue2549NewThread)

			guard, err := issue2549Guard(inst, []*session.Instance{inst}, storage)
			if err != nil {
				t.Fatalf("send refused after the live process rotated its thread: %v", err)
			}
			defer guard.Release()
			if inst.CodexSessionID != issue2549NewThread || guard.fence.codexSessionID != issue2549NewThread {
				t.Fatalf("send bound to %q (fence %q), want the live thread %q",
					inst.CodexSessionID, guard.fence.codexSessionID, issue2549NewThread)
			}
			if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != issue2549NewThread {
				t.Fatalf("persisted identity = %q, want %q", persisted, issue2549NewThread)
			}
			if env, _ := inst.GetTmuxSession().GetEnvironment("CODEX_SESSION_ID"); strings.TrimSpace(env) != issue2549NewThread {
				t.Fatalf("pane CODEX_SESSION_ID = %q, want %q", env, issue2549NewThread)
			}
		})
	}
}

// Ambiguous live evidence (the process holds two threads open, as after /new
// while the old rollout is still open) must not rebind: the send keeps
// refusing as provably unavailable.
func TestIssue2549_AmbiguousLiveThreadsDoNotRebind(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	inst, storage := newIssue2549Instance(t, root, "issue2549_ambiguous", "ambiguous-codex")
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	startRotatedCodexPane(t, root, home, inst, issue2549OldThread, issue2549NewThread, issue2549Other)
	if live := inst.LiveCodexThreadID(); live != "" {
		t.Fatalf("harness: live thread %q, want ambiguous", live)
	}

	_, err := issue2549Guard(inst, []*session.Instance{inst}, storage)
	if !errors.Is(err, errCodexGenerationUnavailable) {
		t.Fatalf("guard error = %v, want generation unavailable", err)
	}
	if inst.CodexSessionID != issue2549OldThread {
		t.Fatalf("ambiguous evidence rebound the identity to %q", inst.CodexSessionID)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != issue2549OldThread {
		t.Fatalf("persisted identity = %q, want unchanged %q", persisted, issue2549OldThread)
	}
}

// A live thread another live session already owns is never adopted.
func TestIssue2549_PeerOwnedLiveThreadIsNotAdopted(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	inst, storage := newIssue2549Instance(t, root, "issue2549_peer", "rotated-codex")
	peer := session.NewInstanceWithTool("peer-codex", inst.ProjectPath, "codex")
	peer.CodexSessionID = issue2549NewThread
	if err := storage.SaveWithGroups([]*session.Instance{inst, peer}, nil); err != nil {
		t.Fatal(err)
	}
	startLegacyCodexPane(t, peer, issue2549NewThread)
	startRotatedCodexPane(t, root, home, inst, issue2549OldThread, issue2549NewThread)

	_, err := issue2549Guard(inst, []*session.Instance{inst, peer}, storage)
	if err == nil || !strings.Contains(err.Error(), "owned by another session") {
		t.Fatalf("guard error = %v, want peer ownership refusal", err)
	}
	if inst.CodexSessionID != issue2549OldThread {
		t.Fatalf("peer-owned thread was adopted: %q", inst.CodexSessionID)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != issue2549OldThread {
		t.Fatalf("persisted identity = %q, want unchanged %q", persisted, issue2549OldThread)
	}
}

// A Guardian review or subagent thread the process holds open is never an
// operator thread (#2529), so it is not adopted either.
func TestIssue2549_SubagentLiveThreadIsNotAdopted(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	writeLegacyCodexRollout(t, home, issue2549OldThread, "21")
	guardian := filepath.Join(home, "sessions", "2026", "10", "08", "rollout-test-"+issue2549NewThread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(guardian), 0o700); err != nil {
		t.Fatal(err)
	}
	header := `{"type":"session_meta","payload":{"id":"` + issue2549NewThread + `","thread_source":"guardian_review","parent_thread_id":"` + issue2549OldThread + `"}}` + "\n"
	if err := os.WriteFile(guardian, []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	inst, storage := newIssue2549Instance(t, root, "issue2549_guardian", "guardian-codex")
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	startRotatedCodexPane(t, root, home, inst, issue2549OldThread, issue2549NewThread)

	guard, err := issue2549Guard(inst, []*session.Instance{inst}, storage)
	if err == nil {
		guard.Release()
	}
	if inst.CodexSessionID != issue2549OldThread {
		t.Fatalf("Guardian thread was adopted as the operator identity: %q", inst.CodexSessionID)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != issue2549OldThread {
		t.Fatalf("persisted identity = %q, want unchanged %q", persisted, issue2549OldThread)
	}
}

func newIssue2549QueuedRecord(t *testing.T) (string, *sendqueue.Record, func(func(*sendqueue.Record)) error) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	rec := &sendqueue.Record{SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, SessionID: "codex-1", Tool: "codex", Message: "hi",
		CreatedAt: now.UTC().Format(time.RFC3339Nano), Deadline: now.Add(30 * time.Minute).UTC().Format(time.RFC3339Nano)}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err == nil {
			*rec = *r
		}
		return err
	}
	return dir, rec, set
}

func stubIssue2549Child(t *testing.T, result string) {
	t.Helper()
	prev := sendChild
	t.Cleanup(func() { sendChild = prev })
	sendChild = func(profile, id, message, resultPath string) (int, func() int, error) {
		_ = os.WriteFile(resultPath, []byte(result), 0o600)
		return 4242, func() int { return 1 }, nil
	}
}

const issue2549UnavailableResult = `{"success":false,"delivery":"acceptance_refused","acceptance_unavailable":true,"code":"INVALID_OPERATION",` +
	`"error":"cannot establish exact Codex turn acceptance: current rollout generation is unavailable"}`

// A refusal that says the identity is provably unavailable is not a busy
// target: once it persists, the queue fails with that reason instead of
// retrying it for the whole 30 minute budget (the report saw 26 attempts). The
// first refusal still retries, so a fresh composer that has not taken its
// thread yet gets a few seconds.
func TestIssue2549_QueueFailsFastOnPersistentUnavailableIdentity(t *testing.T) {
	dir, rec, set := newIssue2549QueuedRecord(t)
	stubIssue2549Child(t, issue2549UnavailableResult)

	const fastLimit = 10
	for attempt := 1; attempt <= fastLimit && rec.State == sendqueue.StateQueued; attempt++ {
		if !typeQueued("", dir, rec, "waiting", "", 0, set) {
			t.Fatal("typeQueued reported a failed write")
		}
		if attempt == 1 && rec.State != sendqueue.StateQueued {
			t.Fatalf("first unavailable refusal must retry: %+v", rec)
		}
	}
	if rec.State != sendqueue.StateFailed {
		t.Fatalf("after %d attempts refused as unavailable identity the send is still %q (%s), want failed", rec.Attempts, rec.State, rec.Reason)
	}
	if !strings.Contains(rec.Reason, "current rollout generation is unavailable") {
		t.Fatalf("failed without the refusal reason: %q", rec.Reason)
	}
}

// Any other acceptance refusal (another send holds the acceptance lock, an
// earlier submission is unresolved) is contention that resolves: it keeps the
// ordinary retry budget.
func TestIssue2549_QueueKeepsRetryingContestedRefusal(t *testing.T) {
	dir, rec, set := newIssue2549QueuedRecord(t)
	stubIssue2549Child(t, `{"success":false,"delivery":"acceptance_refused","code":"INVALID_OPERATION","error":"cannot establish exact Codex turn acceptance: lock held"}`)
	for attempt := 1; attempt <= 12; attempt++ {
		if !typeQueued("", dir, rec, "waiting", "", 0, set) {
			t.Fatal("typeQueued reported a failed write")
		}
		if rec.State != sendqueue.StateQueued {
			t.Fatalf("attempt %d: contested refusal ended the send: %+v", attempt, rec)
		}
	}
}

// The unavailable streak counts consecutive refusals only: a contested
// refusal in between starts it again.
func TestIssue2549_QueueUnavailableStreakResets(t *testing.T) {
	dir, rec, set := newIssue2549QueuedRecord(t)
	contested := `{"success":false,"delivery":"acceptance_refused","error":"lock held"}`
	for attempt := 1; attempt <= 12; attempt++ {
		result := issue2549UnavailableResult
		if attempt%2 == 0 {
			result = contested
		}
		stubIssue2549Child(t, result)
		if !typeQueued("", dir, rec, "waiting", "", 0, set) {
			t.Fatal("typeQueued reported a failed write")
		}
		if rec.State != sendqueue.StateQueued {
			t.Fatalf("attempt %d: alternating refusals ended the send: %+v", attempt, rec)
		}
	}
}
