package web

import (
	"os"
	"path/filepath"
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

	valid := `{"status":"running","session_id":"claude-1","event":"UserPromptSubmit","ts":1735689600}`
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
