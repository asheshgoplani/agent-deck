package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedSubstatePreservesQuotaWithoutPane(t *testing.T) {
	inst := &Instance{ID: "quota", Tool: "claude", Status: StatusError,
		ClaudeSessionID: "conversation", usageLimitSessionID: "conversation",
		usageLimitedCached:   true,
		lastUsageLimitScanAt: time.Now()}
	if got := inst.HistoricalSubstate(); got != SubstateUsageLimit {
		t.Fatalf("cached substate=%s, want usage-limit", got)
	}
	inst.usageLimitedCached = false
	if got := inst.HistoricalSubstate(); got != SubstateNone {
		t.Fatalf("cached substate=%s, want none", got)
	}
}

func TestHistoricalSubstateReadsRecentQuotaFromExactTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("quota-history", resolved, "claude")
	inst.ClaudeSessionID = "quota-conversation"
	inst.Status = StatusError
	inst.tmuxSession = nil
	dir := filepath.Join(GetClaudeConfigDirForInstance(inst), "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, inst.ClaudeSessionID+".jsonl"),
		[]byte(recRateLimitAt(time.Now().UTC().Format(time.RFC3339Nano))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := inst.HistoricalSubstate(); got != SubstateUsageLimit {
		t.Fatalf("historical quota=%s, want usage-limit", got)
	}
}
