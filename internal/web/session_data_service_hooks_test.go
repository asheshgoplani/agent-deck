package web

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDefaultLoadHookStatuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}

	valid := `{"status":"running","session_id":"claude-1","event":"UserPromptSubmit","ts":1735689600,"codex_started_generation":"s:t","codex_completed_generation":"s:t","codex_started_session_id":"s","codex_completed_session_id":"s"}`
	if err := os.WriteFile(filepath.Join(hooksDir, "inst-1.json"), []byte(valid), 0o644); err != nil {
		t.Fatalf("write valid hook file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("write broken hook file: %v", err)
	}

	statuses := defaultLoadHookStatuses()

	got := statuses["inst-1"]
	if got == nil {
		t.Fatalf("expected hook status for inst-1")
	}
	if got.Status != "running" {
		t.Fatalf("expected status running, got %q", got.Status)
	}
	if got.SessionID != "claude-1" {
		t.Fatalf("expected session id claude-1, got %q", got.SessionID)
	}
	if got.Event != "UserPromptSubmit" {
		t.Fatalf("expected event UserPromptSubmit, got %q", got.Event)
	}
	if got.UpdatedAt.Unix() != 1735689600 {
		t.Fatalf("expected timestamp 1735689600, got %d", got.UpdatedAt.Unix())
	}
	if got.CodexStartedGeneration != "s:t" || got.CodexCompletedGeneration != "s:t" ||
		got.CodexStartedSessionID != "s" || got.CodexCompletedSessionID != "s" {
		t.Fatalf("expected Codex evidence parity, got %#v", got)
	}

	if _, ok := statuses["broken"]; ok {
		t.Fatalf("did not expect invalid hook file to be loaded")
	}
}

func TestSessionDataServiceRefreshStatusesAppliesHookData(t *testing.T) {
	inst := &session.Instance{
		ID: "sess-1",
	}

	svc := &SessionDataService{
		loadHookStatuses: func() map[string]*session.HookStatus {
			return map[string]*session.HookStatus{
				"sess-1": {
					Status:    "waiting",
					SessionID: "claude-session-1",
					Event:     "Stop",
					UpdatedAt: time.Now(),
				},
			}
		},
	}

	svc.refreshStatuses([]*session.Instance{inst})

	status, fresh := inst.GetHookStatus()
	if status != "waiting" {
		t.Fatalf("expected waiting hook status, got %q", status)
	}
	if !fresh {
		t.Fatalf("expected hook status to be fresh")
	}
}

// Issue #2582 / review blocker on #2696: the web dashboard reads hook files
// itself and feeds them through UpdateHookStatus. A Hermes delegate_task child
// writes its own session id with platform "subagent"; if this reader drops
// platform, the delegate's pre_llm_call rebinds the entry to the child.
func TestRefreshStatusesHermesSubagentHookKeepsParentBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}

	const parentID = "hermes-parent-session"
	const delegateID = "hermes-delegate-session"

	inst := &session.Instance{ID: "hermes-web-1", Tool: "hermes"}
	inst.UpdateHookStatus(&session.HookStatus{
		Status:    "running",
		SessionID: parentID,
		Event:     "pre_llm_call",
		Platform:  "cli",
		UpdatedAt: time.Now().Add(-time.Minute),
	})
	if inst.HermesSessionID != parentID {
		t.Fatalf("setup: parent turn edge must bind, got %q", inst.HermesSessionID)
	}

	body := `{"status":"running","session_id":"` + delegateID + `","event":"pre_llm_call","ts":` +
		strconv.FormatInt(time.Now().Unix(), 10) + `,"cwd":"/work","platform":"subagent"}`
	if err := os.WriteFile(filepath.Join(hooksDir, inst.ID+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}

	statuses := defaultLoadHookStatuses()
	if hs := statuses[inst.ID]; hs == nil || hs.Platform != "subagent" || hs.Cwd != "/work" {
		t.Fatalf("web hook reader must carry platform and cwd, got %+v", hs)
	}

	svc := &SessionDataService{loadHookStatuses: defaultLoadHookStatuses}
	svc.refreshStatuses([]*session.Instance{inst})

	if inst.HermesSessionID != parentID {
		t.Fatalf("delegate hook via web refresh rebound the entry: got %q, want %q", inst.HermesSessionID, parentID)
	}
}
