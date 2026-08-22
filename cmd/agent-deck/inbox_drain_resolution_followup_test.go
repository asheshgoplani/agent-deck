package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save profile %q: %v", profile, err)
	}
}

func TestInboxDrain_FullIDAndLocalTitleCollisionRefusesWithoutDraining(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	const targetID = "deadbeef-1777000300"
	exact := session.NewInstance("remote-exact-id", t.TempDir())
	exact.ID = targetID
	localTitle := session.NewInstance(targetID, t.TempDir())
	localTitle.ID = "cafefeed-1777000301"
	saveInboxResolutionSessions(t, "personal", exact)
	saveInboxResolutionSessions(t, "work", localTitle)

	if err := session.CommitToInbox(targetID, session.TransitionNotificationEvent{ChildSessionID: "must-survive-title-collision"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var stdout bytes.Buffer
	// The explicit profile is the reproducer: before the fix it bypassed the
	// cross-profile exact-ID pass and ResolveSession chose this profile's title.
	err := runInboxWithProfile(&stdout, []string{"drain", targetID}, "work")
	if err == nil {
		t.Fatalf("ID/title collision silently drained an inbox: %q", stdout.String())
	}
	if got := inboxExitCode(err); got != 3 {
		t.Fatalf("exit code = %d, want 3 (ambiguous): %v", got, err)
	}
	for _, want := range []string{"remote-exact-id", targetID, localTitle.ID, "personal", "work", "Nothing was drained"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("collision error %q missing candidate detail %q", err, want)
		}
	}
	if !session.InboxHasPending(targetID) {
		t.Fatal("ambiguous ID/title collision destructively drained the target inbox")
	}
}

func TestInboxDrain_CorruptProfileAbortsNamesProfileAndPreservesInbox(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	target := session.NewInstance("healthy-target", t.TempDir())
	target.ID = "deadbeef-1777000310"
	saveInboxResolutionSessions(t, "work", target)

	dbPath, err := session.GetDBPathForProfile("aaa-corrupt")
	if err != nil {
		t.Fatalf("corrupt profile path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir corrupt profile: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatalf("write corrupt profile: %v", err)
	}
	if err := session.CommitToInbox(target.ID, session.TransitionNotificationEvent{ChildSessionID: "must-survive-corrupt-profile"}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var stdout bytes.Buffer
	err = runInbox(&stdout, []string{"drain", target.ID})
	if err == nil {
		t.Fatalf("corrupt profile was silently skipped: %q", stdout.String())
	}
	if got := inboxExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1 (storage failure): %v", got, err)
	}
	for _, want := range []string{"aaa-corrupt", "resolution aborted", "Nothing was drained"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("corrupt-profile error %q missing %q", err, want)
		}
	}
	if !session.InboxHasPending(target.ID) {
		t.Fatal("resolution failure on corrupt profile destructively drained the target inbox")
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

func TestInboxDrain_DuplicateFullIDAcrossProfilesRequiresQualifier(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	const duplicate = "deadbeef-1777000200"
	a := session.NewInstance("alpha", t.TempDir())
	b := session.NewInstance("beta", t.TempDir())
	a.ID = duplicate
	b.ID = duplicate
	saveInboxResolutionSessions(t, "aaa", a)
	saveInboxResolutionSessions(t, "bbb", b)
	saveInboxResolutionSessions(t, "work")

	if err := session.CommitToInbox(duplicate, session.TransitionNotificationEvent{ChildSessionID: "must-survive"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var stdout bytes.Buffer
	err := runInbox(&stdout, []string{"drain", duplicate})
	if err == nil {
		t.Fatalf("duplicate full ID silently succeeded and drained shared inbox: %q", stdout.String())
	}
	if got := inboxExitCode(err); got != 3 {
		t.Fatalf("exit code = %d, want 3 (ambiguous): %v", got, err)
	}
	for _, want := range []string{"aaa", "bbb", "-p/--profile", "Nothing was drained"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error %q missing %q", err, want)
		}
	}
	if !session.InboxHasPending(duplicate) {
		t.Fatal("ambiguous duplicate-ID resolution destructively drained the inbox")
	}

	stdout.Reset()
	if err := runInboxWithProfile(&stdout, []string{"drain", duplicate}, "bbb"); err != nil {
		t.Fatalf("qualified duplicate-ID drain: %v", err)
	}
	if !strings.Contains(stdout.String(), "must-survive") {
		t.Fatalf("qualified drain did not return preserved event: %q", stdout.String())
	}
}

// Mutation pin: changing the global exact-ID comparison in
// resolveInboxDrainSessionInProfile from == to strings.HasPrefix makes the two
// remote records capture this shorthand and this test fail with ambiguity.
func TestInboxDrain_ShortPrefixRejectsGlobalPrefixMutation(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	local := session.NewInstance("local", t.TempDir())
	remoteA := session.NewInstance("remote-a", t.TempDir())
	remoteB := session.NewInstance("remote-b", t.TempDir())
	local.ID = "abcdef01-1777000200"
	remoteA.ID = "abcdef02-1777000201"
	remoteB.ID = "abcdef03-1777000202"
	saveInboxResolutionSessions(t, "work", local)
	saveInboxResolutionSessions(t, "aaa", remoteA)
	saveInboxResolutionSessions(t, "bbb", remoteB)

	got, err := resolveInboxDrainSession("abcdef")
	if err != nil {
		t.Fatalf("effective-profile prefix: %v", err)
	}
	if got != local.ID {
		t.Fatalf("resolved %q, want effective-profile session %q", got, local.ID)
	}
}
