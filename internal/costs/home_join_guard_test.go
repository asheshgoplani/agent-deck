package costs_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

// TestSyncFromTranscripts_RefusesTheRealHome: costs sync builds
// ~/.claude/projects/... itself. With HOME at the real home, the way an
// un-isolated test sees it, it must panic in the guard before it stats any
// transcript there.
func TestSyncFromTranscripts_RefusesTheRealHome(t *testing.T) {
	homes := sandboxguard.RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	t.Setenv("HOME", homes[0])
	// costs has no IsolateHome TestMain: clear the overrides a developer
	// shell may carry, so the run does not depend on it.
	for _, k := range sandboxguard.ToolConfigDirEnvs {
		t.Setenv(k, "")
	}
	defer func() {
		msg := fmt.Sprint(recover())
		if !strings.Contains(msg, "sandboxguard:") || !strings.Contains(msg, "through HOME=") {
			t.Fatalf("SyncFromTranscripts with HOME at the real home: recover() = %s, want a guard panic naming HOME", msg)
		}
	}()
	costs.SyncFromTranscripts(nil, nil, []costs.SyncSession{{Tool: "claude", ClaudeSessionID: "sid", ProjectPath: "/p"}})
}
