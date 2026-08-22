package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func saveInboxResolutionSessions(t *testing.T, profile string, instances ...*session.Instance) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage %q: %v", profile, err)
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save profile %q: %v", profile, err)
	}
}

func TestInboxDrain_FullIDResolvesAcrossProfiles(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	target := session.NewInstance("cross-profile-target", t.TempDir())
	saveInboxResolutionSessions(t, "personal", target)
	saveInboxResolutionSessions(t, "work")

	if err := session.CommitToInbox(target.ID, session.TransitionNotificationEvent{ChildSessionID: "cross-profile-child"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var stdout bytes.Buffer
	if err := runInbox(&stdout, []string{"drain", target.ID}); err != nil {
		t.Fatalf("cross-profile full-id drain: %v", err)
	}
	if !strings.Contains(stdout.String(), "cross-profile-child") {
		t.Fatalf("event was not drained: %q", stdout.String())
	}
}

func TestInboxDrain_AmbiguousPrefixPreservesDisambiguationAndExitCode(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	a := session.NewInstance("alpha", t.TempDir())
	b := session.NewInstance("beta", t.TempDir())
	a.ID = "abcdef01-1777000200"
	b.ID = "abcdef02-1777000201"
	saveInboxResolutionSessions(t, "work", a, b)

	var stdout bytes.Buffer
	err := runInbox(&stdout, []string{"drain", "abcdef"})
	if err == nil {
		t.Fatal("ambiguous prefix returned success")
	}
	if got := inboxExitCode(err); got != 3 {
		t.Fatalf("exit code = %d, want 3 (ambiguous)", got)
	}
	for _, want := range []string{"matches multiple sessions", "alpha", "beta", "Use full ID"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error %q missing %q", err, want)
		}
	}
}

func TestInboxDrain_ShortPrefixStaysEffectiveProfileScoped(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	local := session.NewInstance("local", t.TempDir())
	remote := session.NewInstance("remote", t.TempDir())
	local.ID = "abcdef01-1777000200"
	remote.ID = "abcdef02-1777000201"
	saveInboxResolutionSessions(t, "work", local)
	saveInboxResolutionSessions(t, "personal", remote)

	got, err := resolveInboxDrainSession("abcdef")
	if err != nil {
		t.Fatalf("profile-local prefix: %v", err)
	}
	if got != local.ID {
		t.Fatalf("resolved %q, want effective-profile session %q", got, local.ID)
	}
}
