package session

// Issue #1948, review round 2 — the identity rule that keeps two hosts' records
// apart. Everything downstream keys on ChildSessionID, so this is the single
// place provenance enters identity.

import "testing"

func TestIssue1948R2_RemoteScopedChildID(t *testing.T) {
	cases := []struct {
		name, remote, child, want string
	}{
		{"scopes a plain id", "boxb", "nightly-build", "boxb:nightly-build"},
		{"caller prefix remains distinct", "boxb", "boxb:nightly-build", "boxb:boxb:nightly-build"},
		{"a different host is a different id", "boxc", "nightly-build", "boxc:nightly-build"},
		{"no remote leaves the id alone", "", "nightly-build", "nightly-build"},
		{"no child stays empty", "boxb", "", ""},
		{"trims", "  boxb  ", "  nightly-build  ", "boxb:nightly-build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemoteScopedChildID(tc.remote, tc.child); got != tc.want {
				t.Fatalf("RemoteScopedChildID(%q, %q) = %q, want %q", tc.remote, tc.child, got, tc.want)
			}
		})
	}
}

func TestIssue1948R2_SplitRemoteScopedChildID(t *testing.T) {
	remote, child, ok := SplitRemoteScopedChildID("boxb:nightly-build")
	if !ok || remote != "boxb" || child != "nightly-build" {
		t.Fatalf("split gave (%q, %q, %v)", remote, child, ok)
	}

	// A local id has no host prefix and must not be mistaken for one.
	remote, child, ok = SplitRemoteScopedChildID("nightly-build")
	if ok || remote != "" || child != "nightly-build" {
		t.Fatalf("a local id must report no remote, got (%q, %q, %v)", remote, child, ok)
	}
}

// The properties the collision depends on: two hosts' records must differ under
// EVERY identity rule that keys on the child id.
func TestIssue1948R2_ScopedIDsSeparateEveryIdentityRule(t *testing.T) {
	mk := func(child string, status string) TransitionNotificationEvent {
		return TransitionNotificationEvent{
			ChildSessionID: child, ChildTitle: "nightly-build", Profile: "default",
			Kind: transitionKindFinished, DoneStatus: status, DoneSummary: "run",
		}
	}
	b := mk(RemoteScopedChildID("boxb", "nightly-build"), "ok")
	c := mk(RemoteScopedChildID("boxc", "nightly-build"), "fail")

	if EventFingerprint(b) == EventFingerprint(c) {
		t.Fatalf("two hosts' records must not share an EventFingerprint")
	}
	if TurnFingerprint(b) == TurnFingerprint(c) {
		t.Fatalf("two hosts' records must not share a TurnFingerprint")
	}
	collapsed := collapseLastWins([]TransitionNotificationEvent{b, c})
	if len(collapsed) != 2 {
		t.Fatalf("collapseLastWins destroyed one host's record: %+v", collapsed)
	}

	// And the unscoped shape is exactly the collision this guards against.
	ub, uc := mk("nightly-build", "ok"), mk("nightly-build", "fail")
	if len(collapseLastWins([]TransitionNotificationEvent{ub, uc})) != 1 {
		t.Fatalf("premise check failed: unscoped ids were expected to collapse")
	}
}

func TestIssue1952_OriginSeparatesEveryIdentityRule(t *testing.T) {
	mk := func(source string) TransitionNotificationEvent {
		return TransitionNotificationEvent{ChildSessionID: "boxb:nightly-build", SourceRemote: source,
			Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "run"}
	}
	local, remote := mk(""), mk("boxb")
	if EventFingerprint(local) == EventFingerprint(remote) {
		t.Fatal("local and remote records share EventFingerprint")
	}
	if TurnFingerprint(local) == TurnFingerprint(remote) {
		t.Fatal("local and remote records share TurnFingerprint")
	}
	if got := collapseLastWins([]TransitionNotificationEvent{local, remote}); len(got) != 2 {
		t.Fatalf("collapseLastWins merged distinct origins: %+v", got)
	}
}
