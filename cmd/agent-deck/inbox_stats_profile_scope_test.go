package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// inbox stats --all must list only parents of the invoking profile; the
// stats directory is shared by every profile on the host.
func setupInboxStatsTwoProfiles(t *testing.T) (aID, bID string) {
	t.Helper()
	cliInboxTestHome(t)
	a := session.NewInstance("parent-a", t.TempDir())
	a.ID = "aaaa0001-1777000400"
	b := session.NewInstance("parent-b", t.TempDir())
	b.ID = "bbbb0002-1777000401"
	saveInboxResolutionSessions(t, "A", a)
	saveInboxResolutionSessions(t, "B", b)
	for _, id := range []string{a.ID, b.ID} {
		if err := session.BumpInboxStats(id, func(s *session.InboxStats) { s.WakeupsUrgent++ }); err != nil {
			t.Fatal(err)
		}
	}
	return a.ID, b.ID
}

func runInboxStatsAllJSON(t *testing.T, explicitProfile string) []session.InboxStats {
	t.Helper()
	var buf bytes.Buffer
	if err := runInboxStats(&buf, []string{"--all", "--json"}, explicitProfile); err != nil {
		t.Fatal(err)
	}
	var got []session.InboxStats
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	return got
}

func TestInboxStatsAll_ScopedToInvokingProfile(t *testing.T) {
	aID, bID := setupInboxStatsTwoProfiles(t)
	got := runInboxStatsAllJSON(t, "A")
	if len(got) != 1 || got[0].Parent != aID {
		t.Fatalf("inbox stats --all under profile A = %+v; want only %s (profile B's %s leaked)", got, aID, bID)
	}
}

func TestInboxStatsAll_ScopedToEnvProfile(t *testing.T) {
	aID, bID := setupInboxStatsTwoProfiles(t)
	t.Setenv("AGENTDECK_PROFILE", "B")
	got := runInboxStatsAllJSON(t, "")
	if len(got) != 1 || got[0].Parent != bID {
		t.Fatalf("inbox stats --all with AGENTDECK_PROFILE=B = %+v; want only %s (profile A's %s leaked)", got, bID, aID)
	}
}
