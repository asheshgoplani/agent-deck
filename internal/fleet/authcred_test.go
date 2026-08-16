package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Tests for credential-identity grouping of auth-401 holds (#1816).
//
// Every test here drives the real grouping logic through the injectable seams,
// so nothing spawns tmux, writes a sidecar, or reads a real config.toml.

// heldInstance builds an auth-held session attributed to an account slot.
func heldInstance(title, account string) *session.Instance {
	inst := testInstance(title, session.StatusError)
	inst.Account = account
	return inst
}

// dirGrouper returns a grouper whose config-dir chain is a fixed lookup keyed by
// session title, and which treats every instance in held as auth-held.
func dirGrouper(dirs map[string]string, reasons map[string]string) *AuthCredentialGrouper {
	return &AuthCredentialGrouper{
		Hold: func(inst *session.Instance) *session.AuthHoldRecord {
			if inst == nil {
				return nil
			}
			reason, ok := reasons[inst.Title]
			if !ok {
				return nil
			}
			return &session.AuthHoldRecord{InstanceID: inst.ID, Reason: reason}
		},
		ConfigDir: func(inst *session.Instance) string {
			if inst == nil {
				return ""
			}
			return dirs[inst.Title]
		},
	}
}

// allHeld marks every named session as held on a live banner.
func allHeld(titles ...string) map[string]string {
	out := make(map[string]string, len(titles))
	for _, t := range titles {
		out[t] = session.AuthHoldReasonLive
	}
	return out
}

// THE HEADLINE: N sessions on one credential produce ONE escalation, not N.
func TestOneDeadCredentialProducesOneEscalation(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "work"),
		heldInstance("charlie", "work"),
	}
	g := dirGrouper(map[string]string{
		"alpha":   "/home/u/.claude-work",
		"bravo":   "/home/u/.claude-work",
		"charlie": "/home/u/.claude-work",
	}, allHeld("alpha", "bravo", "charlie"))

	sum := g.Summarize(insts)

	if sum.Held != 3 {
		t.Errorf("Held = %d, want 3", sum.Held)
	}
	if sum.Credentials != 1 {
		t.Errorf("Credentials = %d, want 1", sum.Credentials)
	}
	esc := sum.Escalations()
	if len(esc) != 1 {
		t.Fatalf("got %d escalations, want exactly 1: %v", len(esc), esc)
	}
	if !strings.Contains(esc[0], "3 session(s) held") {
		t.Errorf("escalation does not report all 3 sessions: %s", esc[0])
	}
	if !strings.Contains(esc[0], "work") {
		t.Errorf("escalation does not name the account: %s", esc[0])
	}
}

// Two credentials dying at once must NOT collapse into one escalation — the
// aggregation has to keep counting credentials, not just sessions.
func TestTwoCredentialsDyingAtOnceStayTwoEscalations(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "personal"),
		heldInstance("charlie", "work"),
	}
	g := dirGrouper(map[string]string{
		"alpha":   "/home/u/.claude-work",
		"bravo":   "/home/u/.claude-personal",
		"charlie": "/home/u/.claude-work",
	}, allHeld("alpha", "bravo", "charlie"))

	sum := g.Summarize(insts)

	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2", sum.Credentials)
	}
	if len(sum.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(sum.Groups))
	}
	counts := map[string]int{}
	for _, grp := range sum.Groups {
		counts[grp.Credential.ConfigDir] = len(grp.Sessions)
	}
	if counts["/home/u/.claude-work"] != 2 {
		t.Errorf("work credential holds %d sessions, want 2", counts["/home/u/.claude-work"])
	}
	if counts["/home/u/.claude-personal"] != 1 {
		t.Errorf("personal credential holds %d sessions, want 1", counts["/home/u/.claude-personal"])
	}
}

// THE SPLIT DIRECTION: the same account NAME resolving to two different config
// dirs is two different credentials. An account slot with no config_dir block
// falls through to the group/conductor levels, so this is reachable in practice
// — and merging on the name would promise that one re-login fixes both.
func TestSameAccountNameUnderTwoConfigDirsIsTwoCredentials(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "work"),
	}
	g := dirGrouper(map[string]string{
		"alpha": "/home/u/.claude-a",
		"bravo": "/home/u/.claude-b",
	}, allHeld("alpha", "bravo"))

	sum := g.Summarize(insts)

	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2 — the same account name over two config dirs is two credentials", sum.Credentials)
	}
	for _, grp := range sum.Groups {
		if len(grp.Sessions) != 1 {
			t.Errorf("group %s holds %d sessions, want 1", grp.Credential.Key, len(grp.Sessions))
		}
	}
}

// THE MERGE DIRECTION: two account names over ONE config dir share one token
// file, so it is one credential and one re-login really does fix both.
func TestTwoAccountNamesOverOneConfigDirIsOneCredential(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "work-alias"),
	}
	g := dirGrouper(map[string]string{
		"alpha": "/home/u/.claude-shared",
		"bravo": "/home/u/.claude-shared",
	}, allHeld("alpha", "bravo"))

	sum := g.Summarize(insts)

	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1 — one config dir is one token file", sum.Credentials)
	}
	got := sum.Groups[0].Accounts
	if len(got) != 2 || got[0] != "work" || got[1] != "work-alias" {
		t.Errorf("Accounts = %v, want both slot names sorted", got)
	}
	if !strings.Contains(sum.Groups[0].Escalation(), "work, work-alias") {
		t.Errorf("escalation should name both slots: %s", sum.Groups[0].Escalation())
	}
}

// UNKNOWN IS ITS OWN BUCKET: an unattributable session is never folded into an
// attributed credential's group, and its escalation must not claim the sessions
// share a credential.
func TestUnattributableSessionGetsItsOwnBucket(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "work"),
		heldInstance("orphan", ""),
	}
	g := dirGrouper(map[string]string{
		"alpha":  "/home/u/.claude-work",
		"bravo":  "/home/u/.claude-work",
		"orphan": "", // unresolvable
	}, allHeld("alpha", "bravo", "orphan"))

	sum := g.Summarize(insts)

	if sum.Credentials != 1 {
		t.Errorf("Credentials = %d, want 1 — the unknown bucket is not a credential", sum.Credentials)
	}
	if sum.Unattributed != 1 {
		t.Errorf("Unattributed = %d, want 1", sum.Unattributed)
	}
	if len(sum.Groups) != 2 {
		t.Fatalf("got %d groups, want 2 (work + unknown)", len(sum.Groups))
	}
	// The attributed group must not have absorbed the orphan.
	work := sum.Groups[0]
	if !work.Credential.Attributed || len(work.Sessions) != 2 {
		t.Fatalf("attributed group = %+v, want exactly the 2 work sessions", work)
	}
	for _, s := range work.Sessions {
		if s.Title == "orphan" {
			t.Fatal("orphan was folded into the attributed credential's group")
		}
	}
	// Unknown sorts last, and says out loud that it is not one credential.
	unknown := sum.Groups[1]
	if unknown.Credential.Key != UnknownCredentialKey || unknown.Credential.Attributed {
		t.Fatalf("last group = %+v, want the unattributed bucket", unknown)
	}
	esc := unknown.Escalation()
	if !strings.Contains(esc, "NOT known to share one credential") {
		t.Errorf("unknown escalation must not imply a shared credential: %s", esc)
	}
	if strings.Contains(esc, "re-authenticate that credential once") {
		t.Errorf("unknown escalation must not promise a single re-login fixes it: %s", esc)
	}
}

// The dangerous shape of "unknown is not empty": when HOME cannot be resolved,
// the config-dir chain's last resort degrades to a bare relative ".claude" for
// EVERY session at once. Treating that as a key would merge an entire fleet into
// one fabricated credential and report one dead token for what may be many.
func TestRelativeConfigDirNeverBecomesAGroupKey(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("bravo", "personal"),
	}
	g := dirGrouper(map[string]string{
		"alpha": ".claude",
		"bravo": ".claude",
	}, allHeld("alpha", "bravo"))

	sum := g.Summarize(insts)

	if sum.Credentials != 0 {
		t.Errorf("Credentials = %d, want 0 — a relative path does not identify a credential", sum.Credentials)
	}
	if sum.Unattributed != 2 {
		t.Errorf("Unattributed = %d, want 2", sum.Unattributed)
	}
	if len(sum.Groups) != 1 || sum.Groups[0].Credential.Attributed {
		t.Fatalf("groups = %+v, want a single unattributed bucket", sum.Groups)
	}
	if strings.Contains(sum.Groups[0].Escalation(), "re-authenticate that credential once") {
		t.Error("a fabricated credential must never be escalated as a single dead token")
	}
}

// A non-Claude session is unattributable rather than guessed into a Claude
// credential's group.
func TestNonClaudeToolIsUnattributed(t *testing.T) {
	inst := heldInstance("codex-one", "work")
	inst.Tool = "codex"
	g := dirGrouper(map[string]string{"codex-one": "/home/u/.claude-work"}, allHeld("codex-one"))

	sum := g.Summarize([]*session.Instance{inst})

	if sum.Credentials != 0 || sum.Unattributed != 1 {
		t.Fatalf("Credentials = %d, Unattributed = %d; want 0 and 1", sum.Credentials, sum.Unattributed)
	}
}

// A nil instance must not panic or create a phantom credential.
func TestNilInstanceIsSkipped(t *testing.T) {
	g := dirGrouper(map[string]string{"alpha": "/home/u/.claude-work"}, allHeld("alpha"))
	sum := g.Summarize([]*session.Instance{nil, heldInstance("alpha", "work"), nil})
	if sum.Held != 1 || sum.Credentials != 1 {
		t.Fatalf("Held = %d, Credentials = %d; want 1 and 1", sum.Held, sum.Credentials)
	}
}

// Sessions that are not held are not reported at all: the grouping is about
// currently-dead credentials, not history.
func TestUnheldSessionsAreNotGrouped(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("alpha", "work"),
		heldInstance("healthy", "work"),
	}
	g := dirGrouper(map[string]string{
		"alpha":   "/home/u/.claude-work",
		"healthy": "/home/u/.claude-work",
	}, allHeld("alpha")) // only alpha is held

	sum := g.Summarize(insts)

	if sum.Held != 1 {
		t.Fatalf("Held = %d, want 1", sum.Held)
	}
	if len(sum.Groups[0].Sessions) != 1 || sum.Groups[0].Sessions[0].Title != "alpha" {
		t.Errorf("group members = %+v, want only alpha", sum.Groups[0].Sessions)
	}
}

// Zero held sessions is a clean, non-alarming report — not an empty group.
func TestNoHeldSessionsReportsNothingDead(t *testing.T) {
	g := dirGrouper(map[string]string{"alpha": "/home/u/.claude-work"}, map[string]string{})
	sum := g.Summarize([]*session.Instance{heldInstance("alpha", "work")})

	if sum.Held != 0 || sum.Credentials != 0 || len(sum.Groups) != 0 {
		t.Fatalf("summary = %+v, want an empty view", sum)
	}
	if len(sum.Escalations()) != 0 {
		t.Errorf("escalations = %v, want none", sum.Escalations())
	}
	if !strings.Contains(sum.Format(), "no sessions are auth-held") {
		t.Errorf("Format = %q, want the explicit all-clear", sum.Format())
	}
}

// Ordering is deterministic and unknown always sorts last, so a reader who takes
// the first line never lands on the "these may not be related" bucket.
func TestGroupOrderIsDeterministicWithUnknownLast(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("zulu", ""),
		heldInstance("alpha", ""),
		heldInstance("orphan", ""),
	}
	g := dirGrouper(map[string]string{
		"zulu":   "/home/u/.claude-z",
		"alpha":  "/home/u/.claude-a",
		"orphan": "",
	}, allHeld("zulu", "alpha", "orphan"))

	for i := 0; i < 5; i++ {
		sum := g.Summarize(insts)
		if len(sum.Groups) != 3 {
			t.Fatalf("got %d groups, want 3", len(sum.Groups))
		}
		if sum.Groups[0].Credential.ConfigDir != "/home/u/.claude-a" {
			t.Errorf("first group = %s, want the lowest key", sum.Groups[0].Credential.ConfigDir)
		}
		if sum.Groups[2].Credential.Key != UnknownCredentialKey {
			t.Errorf("last group = %s, want the unknown bucket", sum.Groups[2].Credential.Key)
		}
	}
}

// The hold reason travels with each session so a conductor can tell a live
// banner from a session that already exited on one.
func TestHoldReasonIsCarriedPerSession(t *testing.T) {
	insts := []*session.Instance{heldInstance("alpha", "work"), heldInstance("bravo", "work")}
	g := dirGrouper(
		map[string]string{"alpha": "/home/u/.claude-work", "bravo": "/home/u/.claude-work"},
		map[string]string{
			"alpha": session.AuthHoldReasonLive,
			"bravo": session.AuthHoldReasonDeath,
		})

	sum := g.Summarize(insts)

	reasons := map[string]string{}
	for _, s := range sum.Groups[0].Sessions {
		reasons[s.Title] = s.Reason
	}
	if reasons["alpha"] != session.AuthHoldReasonLive || reasons["bravo"] != session.AuthHoldReasonDeath {
		t.Errorf("reasons = %v, want the per-session hold reasons preserved", reasons)
	}
}

// A trailing separator or an unclean path must not split one credential in two.
func TestConfigDirIsCanonicalisedBeforeGrouping(t *testing.T) {
	insts := []*session.Instance{heldInstance("alpha", "work"), heldInstance("bravo", "work")}
	g := dirGrouper(map[string]string{
		"alpha": "/home/u/.claude-work",
		"bravo": "/home/u/./.claude-work/",
	}, allHeld("alpha", "bravo"))

	sum := g.Summarize(insts)

	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1 — the same directory written two ways is one credential", sum.Credentials)
	}
}

// PRIVACY BOUNDARY: grouping is a read. It must not create or write anything,
// and no output may carry token-shaped material.
func TestGroupingWritesNothingAndLeaksNoTokenMaterial(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	before := snapshotTree(t, home)

	insts := []*session.Instance{heldInstance("alpha", "work"), heldInstance("bravo", "work")}
	g := dirGrouper(map[string]string{
		"alpha": filepath.Join(home, ".claude-work"),
		"bravo": filepath.Join(home, ".claude-work"),
	}, allHeld("alpha", "bravo"))

	sum := g.Summarize(insts)
	rendered := sum.Format() + strings.Join(sum.Escalations(), "\n")

	if after := snapshotTree(t, home); after != before {
		t.Errorf("grouping wrote to the data dir:\nbefore=%v\nafter=%v", before, after)
	}
	for _, forbidden := range []string{"sk-ant", "Bearer ", "refresh_token", "access_token", "oauth"} {
		if strings.Contains(strings.ToLower(rendered), strings.ToLower(forbidden)) {
			t.Errorf("rendered output contains %q — credential material must never be emitted:\n%s", forbidden, rendered)
		}
	}
}

// snapshotTree lists every path under root, so a test can assert nothing was
// created. TestMain's IsolateHome makes this a small, private tree.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is not what this test measures
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return strings.Join(paths, "\n")
}

// ---------------------------------------------------------------------------
// PR #1963 review findings P2a / P2b / P2c.
// ---------------------------------------------------------------------------

// sshInstance is a held session running on a remote host.
func sshInstance(title, account, sshHost string) *session.Instance {
	inst := heldInstance(title, account)
	inst.SSHHost = sshHost
	return inst
}

// P2a — THE STORE IS (host, path), NOT path. A local session and an SSH session
// resolving the same config path read two different credential files on two
// different machines. Merging them would report one re-auth as recovering
// sessions it cannot reach.
func TestLocalAndSSHSessionOnSamePathAreSeparateCredentials(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("local-one", "work"),
		sshInstance("remote-one", "work", "box-b"),
	}
	g := dirGrouper(map[string]string{
		"local-one":  "/home/u/.claude-work",
		"remote-one": "/home/u/.claude-work", // SAME path, different machine
	}, allHeld("local-one", "remote-one"))

	sum := g.Summarize(insts)

	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2 — same path on two hosts is two credential stores", sum.Credentials)
	}
	if len(sum.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(sum.Groups))
	}
	for _, grp := range sum.Groups {
		if len(grp.Sessions) != 1 {
			t.Errorf("group %q holds %d sessions, want 1 — the two hosts were merged", grp.Credential.Key, len(grp.Sessions))
		}
	}

	// And the remote one must not tell the operator a local re-login fixes it.
	var remote *CredentialGroup
	for i := range sum.Groups {
		if sum.Groups[i].Credential.IsRemote() {
			remote = &sum.Groups[i]
		}
	}
	if remote == nil {
		t.Fatal("no remote credential group produced")
	}
	if remote.Credential.Host != "box-b" || remote.Credential.HostLabel() != "box-b" {
		t.Errorf("remote host = %q / %q, want box-b", remote.Credential.Host, remote.Credential.HostLabel())
	}
	esc := remote.Escalation()
	if !strings.Contains(esc, "re-authenticating locally will NOT reach it") {
		t.Errorf("remote escalation must not promise a local fix: %s", esc)
	}
	if !strings.Contains(esc, "box-b") {
		t.Errorf("remote escalation must name the host: %s", esc)
	}
}

// P2a, follow-up shape: the SAME account reached over TWO different ssh
// destinations is two stores, not one.
func TestSameAccountOverTwoSSHDestinationsIsTwoCredentials(t *testing.T) {
	insts := []*session.Instance{
		sshInstance("r1", "work", "box-b"),
		sshInstance("r2", "work", "box-c"),
		sshInstance("r3", "work", "box-b"),
	}
	g := dirGrouper(map[string]string{
		"r1": "/home/u/.claude-work",
		"r2": "/home/u/.claude-work",
		"r3": "/home/u/.claude-work",
	}, allHeld("r1", "r2", "r3"))

	sum := g.Summarize(insts)

	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2 (box-b, box-c)", sum.Credentials)
	}
	byHost := map[string]int{}
	for _, grp := range sum.Groups {
		byHost[grp.Credential.Host] = len(grp.Sessions)
	}
	if byHost["box-b"] != 2 || byHost["box-c"] != 1 {
		t.Errorf("sessions by host = %v, want box-b:2 box-c:1", byHost)
	}
}

// P2a, the merge direction still has to work: two sessions on the SAME ssh
// destination and path are one store, so the host dimension has not simply
// disabled aggregation for remote sessions.
func TestSameSSHDestinationAndPathIsOneCredential(t *testing.T) {
	insts := []*session.Instance{
		sshInstance("r1", "work", "box-b"),
		sshInstance("r2", "work", "box-b"),
	}
	g := dirGrouper(map[string]string{
		"r1": "/home/u/.claude-work",
		"r2": "/home/u/.claude-work",
	}, allHeld("r1", "r2"))

	sum := g.Summarize(insts)

	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1 — one host + one path is one store", sum.Credentials)
	}
	if len(sum.Groups[0].Sessions) != 2 {
		t.Errorf("group holds %d sessions, want 2", len(sum.Groups[0].Sessions))
	}
}

// P2a, key hygiene: a host literally named "local" must not collide with the
// local scope, and a blank/whitespace SSHHost is local rather than a distinct
// phantom host.
func TestHostScopeCannotCollideWithLocal(t *testing.T) {
	insts := []*session.Instance{
		heldInstance("plain-local", "work"),
		sshInstance("named-local", "work", "local"),
		sshInstance("blank-host", "work", "   "),
	}
	g := dirGrouper(map[string]string{
		"plain-local": "/home/u/.claude-work",
		"named-local": "/home/u/.claude-work",
		"blank-host":  "/home/u/.claude-work",
	}, allHeld("plain-local", "named-local", "blank-host"))

	sum := g.Summarize(insts)

	// plain-local and blank-host are both local (one store); "local" the
	// hostname is a second, separate store.
	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2 — a host named \"local\" is not the local machine", sum.Credentials)
	}

	var localGroup, sshNamedLocal *CredentialGroup
	for i := range sum.Groups {
		if sum.Groups[i].Credential.IsRemote() {
			sshNamedLocal = &sum.Groups[i]
		} else {
			localGroup = &sum.Groups[i]
		}
	}
	if localGroup == nil || sshNamedLocal == nil {
		t.Fatalf("groups = %+v, want one local store and one ssh store", sum.Groups)
	}
	// A blank/whitespace SSHHost is local, not a phantom third store.
	if len(localGroup.Sessions) != 2 {
		t.Errorf("local store holds %d sessions, want 2 (a whitespace SSHHost is local)", len(localGroup.Sessions))
	}
	if len(sshNamedLocal.Sessions) != 1 {
		t.Errorf("ssh://local store holds %d sessions, want 1", len(sshNamedLocal.Sessions))
	}
	// The load-bearing property: the keys are distinct even though both render
	// their host as the word "local".
	if localGroup.Credential.Key == sshNamedLocal.Credential.Key {
		t.Errorf("key collision: a host named %q produced the same key as the local machine (%s)",
			"local", localGroup.Credential.Key)
	}
}

// P2b — the --group filter must narrow the credential view, like every other
// view. Asking about one group and getting fleet-wide credential state is wrong
// exactly when an operator is narrowing down a problem.
func TestGroupFilterNarrowsTheCredentialSummary(t *testing.T) {
	inGroupA := heldInstance("a-one", "work")
	inGroupA.GroupPath = "team-a"
	nested := heldInstance("a-nested", "work")
	nested.GroupPath = "team-a/sub"
	other := heldInstance("b-one", "personal")
	other.GroupPath = "team-b"

	dirs := map[string]string{
		"a-one":    "/home/u/.claude-work",
		"a-nested": "/home/u/.claude-work",
		"b-one":    "/home/u/.claude-personal",
	}
	held := allHeld("a-one", "a-nested", "b-one")

	t.Run("unfiltered sees the whole fleet", func(t *testing.T) {
		g := dirGrouper(dirs, held)
		sum := g.Summarize([]*session.Instance{inGroupA, nested, other})
		if sum.Held != 3 || sum.Credentials != 2 {
			t.Fatalf("summary = %+v, want 3 held across 2 credentials", sum)
		}
	})

	t.Run("--group narrows to that group and its descendants", func(t *testing.T) {
		g := dirGrouper(dirs, held)
		g.Group = "team-a"
		sum := g.Summarize([]*session.Instance{inGroupA, nested, other})

		if sum.Held != 2 {
			t.Fatalf("Held = %d, want 2 — team-b leaked into a --group team-a view", sum.Held)
		}
		if sum.Credentials != 1 {
			t.Fatalf("Credentials = %d, want 1", sum.Credentials)
		}
		for _, s := range sum.Groups[0].Sessions {
			if s.Title == "b-one" {
				t.Error("a session outside --group appeared in the credential summary")
			}
		}
	})

	t.Run("--group with no members reports an empty view, not the fleet", func(t *testing.T) {
		g := dirGrouper(dirs, held)
		g.Group = "team-zzz"
		sum := g.Summarize([]*session.Instance{inGroupA, nested, other})
		if sum.Held != 0 || len(sum.Groups) != 0 {
			t.Fatalf("summary = %+v, want an empty view for a group with no held sessions", sum)
		}
	})
}

// P2c — attribution must use the IsClaudeCompatible predicate, not a literal
// "claude" comparison. A custom tool wrapping the Claude CLI reads the SAME
// credential file, so a literal match silently drops every aliased session out
// of the group it belongs in.
func TestClaudeCompatibleAliasAggregatesWithClaude(t *testing.T) {
	// A custom tool declaring compatible_with = "claude".
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		t.Skipf("LoadUserConfig unavailable: %v", err)
	}
	if cfg.Tools == nil {
		cfg.Tools = map[string]session.ToolDef{}
	}
	cfg.Tools["claude_wrapper"] = session.ToolDef{
		Command:        "claude-wrapper",
		CompatibleWith: "claude",
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if !session.IsClaudeCompatible("claude_wrapper") {
		t.Fatal("fixture is wrong: claude_wrapper is not Claude-compatible")
	}

	plain := heldInstance("plain", "work")
	aliased := heldInstance("aliased", "work")
	aliased.Tool = "claude_wrapper"

	g := dirGrouper(map[string]string{
		"plain":   "/home/u/.claude-work",
		"aliased": "/home/u/.claude-work",
	}, allHeld("plain", "aliased"))

	sum := g.Summarize([]*session.Instance{plain, aliased})

	if sum.Unattributed != 0 {
		t.Fatalf("Unattributed = %d, want 0 — the aliased session was dropped out of the aggregation", sum.Unattributed)
	}
	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1", sum.Credentials)
	}
	if len(sum.Groups[0].Sessions) != 2 {
		t.Fatalf("group holds %d sessions, want 2 — the alias did not aggregate with claude", len(sum.Groups[0].Sessions))
	}
	if len(sum.Escalations()) != 1 {
		t.Errorf("got %d escalations, want 1", len(sum.Escalations()))
	}
}

// P2c, the other half: a tool that is NOT Claude-compatible still goes to the
// unknown bucket. The predicate must not have widened attribution to everything.
func TestNonCompatibleCustomToolStaysUnattributed(t *testing.T) {
	inst := heldInstance("codexish", "work")
	inst.Tool = "some-other-tool"
	g := dirGrouper(map[string]string{"codexish": "/home/u/.claude-work"}, allHeld("codexish"))

	sum := g.Summarize([]*session.Instance{inst})

	if sum.Credentials != 0 || sum.Unattributed != 1 {
		t.Fatalf("Credentials = %d, Unattributed = %d; want 0 and 1", sum.Credentials, sum.Unattributed)
	}
}
