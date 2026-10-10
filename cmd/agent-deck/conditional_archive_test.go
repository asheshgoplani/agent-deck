package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestConditionalArchiveAcceptanceLockExcludesSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock, err := session.AcquireCodexAcceptanceLock("archive-thread", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if other, err := session.AcquireCodexAcceptanceLock("archive-thread", 10*time.Millisecond); err == nil {
		other.Release()
		t.Fatal("concurrent acceptance passed the conditional archive window")
	}
	lock.Release()
	other, err := session.AcquireCodexAcceptanceLock("archive-thread", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
}

func TestConditionalArchiveClaudeLockExcludesSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	lock, err := session.AcquireSendLock("archive-child", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if other, err := session.AcquireSendLock("archive-child", 10*time.Millisecond); err == nil {
		other.Release()
		t.Fatal("concurrent Claude send passed the conditional archive window")
	}
	lock.Release()
	other, err := session.AcquireSendLock("archive-child", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
}

func TestConditionalArchiveRefusesUnsafeTargets(t *testing.T) {
	for _, tc := range []struct {
		name, parent, version string
		status                session.Status
	}{
		{"no parent", "", "version", session.StatusWaiting},
		{"no version", "parent", "", session.StatusWaiting},
		{"wrong parent", "other", "version", session.StatusWaiting},
		{"running worker", "parent", "version", session.StatusRunning},
		{"unversioned output", "parent", "version", session.StatusWaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := &session.Instance{ID: "child", ParentSessionID: "parent", Tool: "codex", Status: tc.status}
			if err := validateConditionalArchive(inst, nil, tc.parent, tc.version); err == nil {
				t.Fatal("unsafe conditional archival was accepted")
			}
		})
	}
}

func TestConditionalArchiveFencesLatestCodexTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "10", "08", "rollout-test-thread-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	completed := "{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"t1\"}}\n" +
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_complete\",\"turn_id\":\"t1\",\"last_agent_message\":\"report\"}}\n"
	if err := os.WriteFile(path, []byte(completed), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "c", Tool: "codex", ParentSessionID: "p",
		CodexSessionID: "thread-test", Status: session.StatusWaiting}
	version := inst.ResponseContentVersion(nil)
	if err := validateConditionalArchive(inst, nil, "p", version); err != nil {
		t.Fatalf("completed exact turn refused: %v", err)
	}
	generation, err := inst.LatestCodexTurnGeneration()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := session.PrepareCodexSubmissionMarker(inst.ID, inst.CodexSessionID, generation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConditionalArchive(inst, nil, "p", version); err == nil {
		t.Fatal("unresolved no-wait send was accepted for archival")
	}
	if err := session.ClearCodexSubmissionMarker(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(completed+
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"task_started\",\"turn_id\":\"t2\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateConditionalArchive(inst, nil, "p", version); err == nil {
		t.Fatal("old transcript version was accepted")
	}
	if err := validateConditionalArchive(inst, nil, "p", inst.ResponseContentVersion(nil)); err == nil {
		t.Fatal("new incomplete turn was accepted using old completed response")
	}
}

func TestConditionalArchiveFencesClaudeFlushedTerminalReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	project := filepath.Join(home, "project")
	path := filepath.Join(home, "projects", session.ConvertToClaudeDirName(project), "conversation.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	completed := "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"===AGENTDECK_DONE=== status=ok summary=complete\"}]}}\n"
	if err := os.WriteFile(path, []byte(completed), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "c", Tool: "claude", ParentSessionID: "p",
		ProjectPath: project, ClaudeSessionID: "conversation", Status: session.StatusWaiting}
	version := inst.ResponseContentVersion(nil)
	if err := validateConditionalArchive(inst, nil, "p", version); err != nil {
		t.Fatalf("flushed Claude terminal report refused: %v", err)
	}
	if err := os.WriteFile(path, []byte(completed+
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"new prompt\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateConditionalArchive(inst, nil, "p", inst.ResponseContentVersion(nil)); err == nil {
		t.Fatal("pending new Claude prompt was accepted on an old sentinel")
	}
}
