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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group([]*session.Instance{inst})

	if sum.Credentials != 0 || sum.Unattributed != 1 {
		t.Fatalf("Credentials = %d, Unattributed = %d; want 0 and 1", sum.Credentials, sum.Unattributed)
	}
}

// A nil instance must not panic or create a phantom credential.
func TestNilInstanceIsSkipped(t *testing.T) {
	g := dirGrouper(map[string]string{"alpha": "/home/u/.claude-work"}, allHeld("alpha"))
	sum := g.Group([]*session.Instance{nil, heldInstance("alpha", "work"), nil})
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

	sum := g.Group(insts)

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
	sum := g.Group([]*session.Instance{heldInstance("alpha", "work")})

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
		sum := g.Group(insts)
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

	sum := g.Group(insts)

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

	sum := g.Group(insts)

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

	sum := g.Group(insts)
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
