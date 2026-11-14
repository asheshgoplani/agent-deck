// Regression coverage for the hook rebind gate (#661/#856) after a switch
// leaves the bound conversation under more than one project key (ASK-92,
// review of the limit-guard fix). The transcript is append-only, so the gate
// must weigh the bound conversation by its largest copy and date it by its
// newest. It used the newest copy for both, so touching a stale short copy
// shrank the conversation and flipped the decision from reject to rebind.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	gateBoundSID     = "5ea244ce-0000-0000-0000-0000000a5b92"
	gateCandidateSID = "2266314c-0000-0000-0000-0000000a5b92"
)

// writeJSONLOfSize writes one user record for sessionID padded to exactly size bytes.
func writeJSONLOfSize(t *testing.T, path, sessionID string, size int) {
	t.Helper()
	head := fmt.Sprintf(`{"type":"user","sessionId":%q,"text":"`, sessionID)
	tail := "\"}\n"
	pad := size - len(head) - len(tail)
	require.Positive(t, pad)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(head+strings.Repeat("x", pad)+tail), 0o600))
}

type rebindGateFixture struct {
	inst      *Instance
	largePath string // realpath key: the live, append-only copy
	smallPath string // typed key: a stale short copy
}

// newRebindGateFixture builds a symlinked project whose bound conversation has
// a 220200-byte live copy under the realpath key and a 705-byte stale copy
// under the typed key, plus a 2000-byte candidate conversation. When
// touchStale is set the stale copy is touched so it becomes the newest file.
func newRebindGateFixture(t *testing.T, touchStale bool) *rebindGateFixture {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	configDir := filepath.Join(tmpHome, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	realDir := filepath.Join(tmpHome, "real", "project")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	typed := filepath.Join(tmpHome, "link")
	require.NoError(t, os.Symlink(realDir, typed))
	resolved, err := filepath.EvalSymlinks(typed)
	require.NoError(t, err)
	realKey, typedKey := ConvertToClaudeDirName(resolved), ConvertToClaudeDirName(typed)
	require.NotEqual(t, realKey, typedKey)

	inst := NewInstanceWithTool("rebind-gate-largest-copy", typed, "claude")
	require.Equal(t, configDir, GetClaudeConfigDirForInstance(inst))
	f := &rebindGateFixture{
		inst:      inst,
		largePath: filepath.Join(configDir, "projects", realKey, gateBoundSID+".jsonl"),
		smallPath: filepath.Join(configDir, "projects", typedKey, gateBoundSID+".jsonl"),
	}
	candidatePath := filepath.Join(configDir, "projects", realKey, gateCandidateSID+".jsonl")
	writeJSONLOfSize(t, f.largePath, gateBoundSID, 220200)
	writeJSONLOfSize(t, f.smallPath, gateBoundSID, 705)
	writeJSONLOfSize(t, candidatePath, gateCandidateSID, 2000)

	now := time.Now()
	stale, live := now.Add(-12*time.Hour), now.Add(-time.Second)
	require.NoError(t, os.Chtimes(f.smallPath, stale, stale))
	require.NoError(t, os.Chtimes(f.largePath, live, live))
	require.NoError(t, os.Chtimes(candidatePath, now, now))
	if touchStale {
		require.NoError(t, os.Chtimes(f.smallPath, now, now))
	}
	inst.ClaudeSessionID = gateBoundSID
	return f
}

func TestRebindGate_TouchedStaleShortCopyStillRejects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		touchStale bool
	}{
		{"stale copy untouched", false},
		{"stale copy touched", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRebindGateFixture(t, tc.touchStale)
			f.inst.UpdateHookStatus(&HookStatus{
				Status:    "running",
				SessionID: gateCandidateSID,
				Event:     "UserPromptSubmit",
				UpdatedAt: time.Now(),
			})
			require.Equal(t, gateBoundSID, f.inst.ClaudeSessionID,
				"a 2000-byte candidate must not replace a 220200-byte conversation that is still written")
			require.True(t, hasRejectReason(readLifecycleEvents(t), "candidate_has_less_conversation_data"))
		})
	}
}

func TestRebindGate_SizeFromLargestCopyMtimeFromNewest(t *testing.T) {
	f := newRebindGateFixture(t, true)
	require.Equal(t, int64(220200), sessionConversationByteSize(f.inst, gateBoundSID),
		"size must come from the largest copy, not the newest")
	small, err := os.Stat(f.smallPath)
	require.NoError(t, err)
	require.True(t, sessionConversationMtime(f.inst, gateBoundSID).Equal(small.ModTime()),
		"mtime must come from the newest copy")
}
