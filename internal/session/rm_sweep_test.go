package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRm_SweepsConductorInboxes is the regression test for issue #910:
// after `agent-deck rm <child>`, the per-conductor inbox JSONL must no
// longer contain any events keyed by that child_session_id, otherwise the
// conductor replays "delivery_result: deferred_target_busy" lines for a
// child that no longer exists.
func TestRm_SweepsConductorInboxes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	ResetInboxFingerprintCacheForTest()

	// Three conductors. Two carry events for the soon-to-be-removed child,
	// one carries an unrelated event that must survive the sweep.
	deadChild := "child-doomed"
	keepChild := "child-survivor"
	conductors := []string{"conductor-alpha", "conductor-bravo", "conductor-charlie"}

	mkEvent := func(child string, idx int) TransitionNotificationEvent {
		return TransitionNotificationEvent{
			ChildSessionID:  child,
			ChildTitle:      "worker",
			Profile:         "_test",
			FromStatus:      "running",
			ToStatus:        "waiting",
			Timestamp:       time.Now().Add(time.Duration(idx) * time.Second),
			TargetSessionID: conductors[idx%len(conductors)],
			TargetKind:      "conductor",
			DeliveryResult:  transitionDeliveryDeferred,
		}
	}

	for i := 0; i < 3; i++ {
		require.NoError(t, WriteInboxEvent(conductors[0], mkEvent(deadChild, i)))
	}
	require.NoError(t, WriteInboxEvent(conductors[1], mkEvent(deadChild, 9)))
	require.NoError(t, WriteInboxEvent(conductors[2], mkEvent(keepChild, 0)))

	swept, err := SweepInboxesForChildSession(deadChild)
	require.NoError(t, err)
	if swept != 4 {
		t.Fatalf("expected 4 inbox lines swept, got %d", swept)
	}

	// The dead child's events are gone from every inbox.
	for _, c := range conductors {
		path := InboxPathFor(c)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read inbox %s: %v", c, err)
		}
		if len(data) > 0 && containsBytes(data, []byte(deadChild)) {
			t.Fatalf("inbox %s still references %q: %s", c, deadChild, data)
		}
	}

	// The unrelated event is preserved.
	keepData, err := os.ReadFile(InboxPathFor(conductors[2]))
	require.NoError(t, err)
	if !containsBytes(keepData, []byte(keepChild)) {
		t.Fatalf("unrelated event was swept (data=%q)", keepData)
	}
}

// TestRm_SweepsNotifyStateLedger is the matching regression test for the
// dedup ledger half of issue #910. The ledger at runtime/transition-notify-
// state.json must drop the doomed child's record on rm; survivors stay put.
func TestRm_SweepsNotifyStateLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	// Seed the ledger with records for two children. We expect rm to drop
	// just one of them.
	statePath := transitionNotifyStatePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	seed := transitionNotifyState{
		Records: map[string]transitionNotifyRecord{
			"child-doomed":   {From: "running", To: "waiting", At: time.Now().Unix()},
			"child-survivor": {From: "running", To: "idle", At: time.Now().Unix()},
		},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, raw, 0o644))

	removed, err := RemoveNotifyStateRecord("child-doomed")
	require.NoError(t, err)
	if !removed {
		t.Fatalf("RemoveNotifyStateRecord('child-doomed') returned false; expected true")
	}

	// Read back; doomed must be gone, survivor must remain.
	got, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var after transitionNotifyState
	require.NoError(t, json.Unmarshal(got, &after))
	if _, present := after.Records["child-doomed"]; present {
		t.Fatalf("doomed record still in ledger: %s", got)
	}
	if _, present := after.Records["child-survivor"]; !present {
		t.Fatalf("survivor record was dropped: %s", got)
	}

	// Idempotency: a second sweep on a non-existent child returns false, no error.
	removedAgain, err := RemoveNotifyStateRecord("child-doomed")
	require.NoError(t, err)
	if removedAgain {
		t.Fatalf("second RemoveNotifyStateRecord returned true; expected false (idempotent)")
	}
}

// containsBytes is a small helper to avoid pulling in bytes.Contains imports
// in the test fixtures above (keeps the diff focused).
func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
