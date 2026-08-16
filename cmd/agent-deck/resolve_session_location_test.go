package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ResolveSession is the CLI's identity lookup, and it exists at the merge base —
// so every test in this file compiles against the unfixed tree and fails there
// for the reason the issue describes.
//
// It covers #1852 site 1 (a remote session addressed by the path it runs at, and
// a local placeholder no longer matching every remote session at once) plus the
// two addressing regressions the previous attempt at this epic shipped:
// F3 (a title held twice silently returning the first holder) and F4 (a bare
// local path ceasing to resolve because a remote session mirrors its path).

// --- Site 1: ResolveSession path match ---------------------------------------

// TestResolveSession_RemotePathIsAddressable pins the first effect: a remote
// session could not be addressed by the path it actually runs at, because that
// string is never stored in ProjectPath.
func TestResolveSession_RemotePathIsAddressable(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "app-a", "alice@host-a", "/srv/app-a"),
		{ID: "bbbbbbbbbbbbbbbb", Title: "local", ProjectPath: "/home/dev/local"},
	}

	inst, errMsg, _ := ResolveSession("/srv/app-a", instances)
	if inst == nil {
		t.Fatalf("ResolveSession(/srv/app-a) did not find the remote session running there: %s", errMsg)
	}
	if inst.ID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("ResolveSession(/srv/app-a) = %s, want the remote session", inst.ID)
	}
}

// TestResolveSession_ExplicitHostPathForm addresses a remote session
// unambiguously when two hosts export the same remote directory.
func TestResolveSession_ExplicitHostPathForm(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "app-a", "alice@host-a", "/srv/app"),
		sshInstance("bbbbbbbbbbbbbbbb", "app-b", "bob@host-b", "/srv/app"),
	}

	// The bare path is genuinely ambiguous across two hosts...
	if _, _, code := ResolveSession("/srv/app", instances); code != ErrCodeAmbiguous {
		t.Fatalf("bare /srv/app should be AMBIGUOUS across two hosts, got code %q", code)
	}

	// ...and host:path disambiguates it.
	inst, errMsg, _ := ResolveSession("bob@host-b:/srv/app", instances)
	if inst == nil {
		t.Fatalf("ResolveSession(bob@host-b:/srv/app) failed: %s", errMsg)
	}
	if inst.ID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("ResolveSession(bob@host-b:/srv/app) = %s, want bbbbbbbbbbbbbbbb", inst.ID)
	}
}

// TestResolveSession_PlaceholderDoesNotMatchRemoteSessions pins the second
// effect: several remote sessions sharing one placeholder all matched when the
// local placeholder path was passed, producing a spurious "path has multiple
// sessions" error for sessions that are not in the same place.
func TestResolveSession_PlaceholderDoesNotMatchRemoteSessions(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "app-a", "alice@host-a", "/srv/app-a"),
		sshInstance("bbbbbbbbbbbbbbbb", "app-b", "bob@host-b", "/opt/app-b"),
		{ID: "cccccccccccccccc", Title: "here", ProjectPath: controllerCWD},
	}

	inst, errMsg, code := ResolveSession(controllerCWD, instances)
	if code == ErrCodeAmbiguous {
		t.Fatalf("the controller's own directory was reported ambiguous because remote placeholders matched: %s", errMsg)
	}
	if inst == nil {
		t.Fatalf("ResolveSession(%s) = nil (%s); the one local session there should resolve", controllerCWD, errMsg)
	}
	if inst.ID != "cccccccccccccccc" {
		t.Fatalf("ResolveSession(%s) = %s, want the local session cccccccccccccccc", controllerCWD, inst.ID)
	}
}

// TestResolveSession_AmbiguityMessageNamesLocations keeps the error actionable
// for remote sessions: naming the local placeholder tells the user nothing.
func TestResolveSession_AmbiguityMessageNamesLocations(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "app", "alice@host-a", "/srv/app"),
		sshInstance("bbbbbbbbbbbbbbbb", "app-two", "bob@host-b", "/srv/app"),
	}

	_, errMsg, code := ResolveSession("/srv/app", instances)
	if code != ErrCodeAmbiguous {
		t.Fatalf("expected AMBIGUOUS, got %q", code)
	}
	for _, want := range []string{"alice@host-a:/srv/app", "bob@host-b:/srv/app"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("ambiguity message does not name %q:\n%s", want, errMsg)
		}
	}
}

// TestResolveSession_LocalPathStillResolves guards the common case.
func TestResolveSession_LocalPathStillResolves(t *testing.T) {
	instances := []*session.Instance{
		{ID: "aaaaaaaaaaaaaaaa", Title: "local", ProjectPath: "/home/dev/local"},
	}
	inst, errMsg, _ := ResolveSession("/home/dev/local", instances)
	if inst == nil || inst.ID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("local path lookup regressed: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_TitleHeldTwiceIsAmbiguous is review finding F3.
//
// Location-aware duplicate detection deliberately allows one title at two
// DIFFERENT locations — two `add --ssh` runs from one controller directory
// without -t keep the same directory-derived title. The previous attempt made
// that state reachable while ResolveSession's title branch still returned the
// FIRST exact match, so `agent-deck session <title> stop` acted on an arbitrary
// one of two sessions on two different hosts, silently. Ambiguity must be an
// error, not a coin flip.
func TestResolveSession_TitleHeldTwiceIsAmbiguous(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "proj", "alice@host-a", "/srv/app-a"),
		sshInstance("bbbbbbbbbbbbbbbb", "proj", "bob@host-b", "/opt/app-b"),
	}

	inst, errMsg, code := ResolveSession("proj", instances)
	if inst != nil {
		t.Fatalf("ResolveSession returned session %s for a title held by two sessions on two hosts — a stop/send/output would hit an arbitrary one", inst.ID)
	}
	if code != ErrCodeAmbiguous {
		t.Fatalf("code = %q, want %q", code, ErrCodeAmbiguous)
	}
	for _, want := range []string{"alice@host-a:/srv/app-a", "bob@host-b:/opt/app-b"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("ambiguity message does not name %q, so the user cannot pick one:\n%s", want, errMsg)
		}
	}
}

// TestResolveSession_UniqueTitleStillResolves guards the overwhelmingly common
// case against the fix above.
func TestResolveSession_UniqueTitleStillResolves(t *testing.T) {
	instances := []*session.Instance{
		{ID: "aaaaaaaaaaaaaaaa", Title: "alpha", ProjectPath: "/tmp/a"},
		{ID: "bbbbbbbbbbbbbbbb", Title: "beta", ProjectPath: "/tmp/b"},
	}
	inst, errMsg, _ := ResolveSession("alpha", instances)
	if inst == nil || inst.ID != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("unique title lookup regressed: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_BareLocalPathPrefersLocalSession is review finding F4.
//
// Making a bare path match SSHRemotePath is what lets `agent-deck session
// /srv/app-a` reach the remote session running there (#1852 site 1). The cost,
// if unmanaged, is that `agent-deck <path>` — the documented way to address the
// session in the current directory — starts reporting AMBIGUOUS as soon as an
// unrelated remote session runs at the same absolute path, and remote paths
// routinely mirror local ones.
//
// A path typed on THIS machine means this machine.
func TestResolveSession_BareLocalPathPrefersLocalSession(t *testing.T) {
	local := &session.Instance{ID: "aaaaaaaaaaaaaaaa", Title: "local-app", ProjectPath: "/home/dev/app"}
	remote := sshInstance("bbbbbbbbbbbbbbbb", "remote-app", "alice@host-a", "/home/dev/app")

	inst, errMsg, code := ResolveSession("/home/dev/app", []*session.Instance{local, remote})
	if inst == nil {
		t.Fatalf("a bare local path stopped resolving because a remote session runs at the same absolute path: code=%q %s", code, errMsg)
	}
	if inst.ID != local.ID {
		t.Fatalf("bare path resolved to %s, want the LOCAL session %s", inst.ID, local.ID)
	}

	// The remote session at that path stays addressable through the explicit form.
	inst, errMsg, _ = ResolveSession("alice@host-a:/home/dev/app", []*session.Instance{local, remote})
	if inst == nil || inst.ID != remote.ID {
		t.Fatalf("the remote session at a mirrored path is not addressable explicitly: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_BareRemotePathStillResolvesWhenNoLocalSessionIsThere keeps
// the #1852 site-1 feature working: preferring local matches must not mean
// "ignore remote matches".
func TestResolveSession_BareRemotePathStillResolvesWhenNoLocalSessionIsThere(t *testing.T) {
	instances := []*session.Instance{
		{ID: "aaaaaaaaaaaaaaaa", Title: "local", ProjectPath: "/home/dev/other"},
		sshInstance("bbbbbbbbbbbbbbbb", "remote", "alice@host-a", "/srv/app-a"),
	}
	inst, errMsg, _ := ResolveSession("/srv/app-a", instances)
	if inst == nil || inst.ID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("a bare remote path with no local session at it should resolve to the remote session: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_ExplicitLocationBeatsAFreeTextTitle closes the hole the
// round-2 cross-model review found: the ambiguity messages advertise the
// [user@]host:/path form as the way to pick one session, but a TITLE is free
// text, so a session titled "bob@host-b:/opt/app-b" could shadow the session
// actually running there — the supposedly unambiguous answer selecting the wrong
// session. A session cannot occupy a location by accident.
func TestResolveSession_ExplicitLocationBeatsAFreeTextTitle(t *testing.T) {
	decoy := &session.Instance{ID: "dddddddddddddddd", Title: "bob@host-b:/opt/app-b", ProjectPath: "/tmp/decoy"}
	real := sshInstance("bbbbbbbbbbbbbbbb", "app-b", "bob@host-b", "/opt/app-b")

	inst, errMsg, _ := ResolveSession("bob@host-b:/opt/app-b", []*session.Instance{decoy, real})
	if inst == nil {
		t.Fatalf("explicit location form did not resolve: %s", errMsg)
	}
	if inst.ID != real.ID {
		t.Fatalf("a session TITLED like a location shadowed the session actually running there (got %s, want %s)", inst.ID, real.ID)
	}
}

// TestResolveSession_ExplicitLocationYieldsToTitleWhenNothingRunsThere: the
// precedence must not make an existing session unaddressable. When no session
// runs at the named location, the identifier can only have meant a title or ID.
func TestResolveSession_ExplicitLocationYieldsToTitleWhenNothingRunsThere(t *testing.T) {
	oddTitle := &session.Instance{ID: "dddddddddddddddd", Title: "bob@host-b:/opt/app-b", ProjectPath: "/tmp/decoy"}

	inst, errMsg, _ := ResolveSession("bob@host-b:/opt/app-b", []*session.Instance{oddTitle})
	if inst == nil || inst.ID != oddTitle.ID {
		t.Fatalf("a session whose title looks like a location became unaddressable: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_RemoteHomeIsAddressableByTheStringWePrint ties the
// addressing surface to the canonical-path rule. `add --ssh alice@host-a` with
// no --remote-path stores "", and every message prints that as "alice@host-a:~".
// If "~" did not fold back to "", the ambiguity messages would hand the user an
// identifier that answers NOT_FOUND.
func TestResolveSession_RemoteHomeIsAddressableByTheStringWePrint(t *testing.T) {
	remote := sshInstance("aaaaaaaaaaaaaaaa", "home-session", "alice@host-a", "")

	// "alice@host-a:~" is the literal string every duplicate, rename and
	// ambiguity message prints for a session registered with no --remote-path.
	// (That rendering is pinned separately by TestLocationString_RoundTrips… in
	// internal/session.)
	const rendered = "alice@host-a:~"

	inst, errMsg, _ := ResolveSession(rendered, []*session.Instance{remote})
	if inst == nil || inst.ID != remote.ID {
		t.Fatalf("the identifier the CLI prints does not resolve back to the session: inst=%v err=%s", inst, errMsg)
	}
}

// TestResolveSession_IDPrefixStillWins guards the branch order: an ID prefix
// must keep working, and it is checked after titles as before.
func TestResolveSession_IDPrefixStillWins(t *testing.T) {
	instances := []*session.Instance{
		{ID: "abcdef123456789", Title: "one", ProjectPath: "/tmp/a"},
	}
	inst, errMsg, _ := ResolveSession("abcdef", instances)
	if inst == nil || inst.ID != "abcdef123456789" {
		t.Fatalf("ID prefix lookup regressed: inst=%v err=%s", inst, errMsg)
	}
}
