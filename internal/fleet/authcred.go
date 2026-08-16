package fleet

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Credential-identity grouping for auth-held sessions (#1816).
//
// WHY THIS SITS NEXT TO authgate.go AND NOT IN A PACKAGE OF ITS OWN.
// SubstateAuthGate already aggregates auth-401 across a fleet, but only in one
// direction and only for one purpose: it counts auth-failed BOOTS during a
// recovery sweep so the sweep can halt itself. Two things it does not do, and
// that a conductor watching a fleet actually needs:
//
//  1. It counts sessions, not credentials. Ten sessions booting into a 401 is
//     reported as "10 sessions" whether that is one dead token or three.
//  2. The count exists only inside a sweep. Nothing can ask "how many distinct
//     dead credentials are behind these N held sessions?" without re-deriving
//     it from N per-session substates — which is the N-escalations problem in
//     the first place.
//
// This file adds credential identity as a first-class thing and hands it to
// BOTH callers through one accumulator (credAccumulator), so the sweep-scoped
// tally and the outside-the-sweep query can never drift about what counts as
// "one credential".
//
// # What identifies a credential (and what deliberately does not)
//
// The grouping key is a CREDENTIAL STORE: the pair (host, resolved Claude config
// directory). The directory comes from session.GetClaudeConfigDirForInstance's
// priority chain; the host is the SSH destination, or "local". Both are
// identifiers, never token material: no token, no refresh token, no credential
// file content, and no hash of any of them is read, compared, logged, or
// persisted anywhere in this file. Grouping is computed in memory, per
// invocation, and thrown away.
//
// THE HOST IS PART OF THE IDENTITY, and leaving it out was a real defect. A
// local session and an SSH session can resolve the SAME config path while
// reading two completely different credential files on two different machines.
// Keying on the path alone merged them, so one re-login would have been reported
// as recovering sessions it cannot reach — the aggregation asserting a fix that
// silently does not hold for half the fleet. Same rule as the unknown bucket,
// arriving from the other side: there, one bucket was folded into another; here,
// two genuinely different stores were folded into one. Both are the aggregation
// lying about what it grouped, and on this feature that lie IS the product.
//
// What the host component does and does not claim: two sessions agreeing on both
// host and path are on one credential store; two disagreeing on host are not.
// The local resolution is NOT claimed to be the remote file's true location —
// only to distinguish stores. When in doubt this splits rather than merges,
// because over-aggregation is the direction that misleads an operator into
// thinking one re-auth was enough.
//
// Keying on the directory rather than on Instance.Account is what makes the two
// awkward shapes come out right, and both are real:
//
//   - THE SAME ACCOUNT NAME UNDER TWO CONFIG DIRS IS TWO CREDENTIALS. An account
//     slot with no [profiles.<account>.claude].config_dir block falls THROUGH to
//     the conductor/group/env/profile levels (see resolveClaudeConfigDir), so two
//     sessions both naming account "work" in different groups can be running two
//     different tokens. Keying on the name would merge them and tell the operator
//     one re-login fixes both. It would not.
//   - TWO ACCOUNT NAMES OVER ONE CONFIG DIR IS ONE CREDENTIAL. Distinct slots
//     pointing at the same directory share one token file: one re-login really
//     does fix every session behind them, and reporting two dead credentials
//     would be over-counting the outage.
//
// The account names are kept for DISPLAY (that is what the operator types to fix
// it), never as the identity.
//
// # Unknown is a bucket, not a default
//
// A session whose credential cannot be attributed goes into its own bucket and
// is NEVER folded into an attributed group. The three ways attribution fails are
// all reachable: a nil instance, a non-Claude tool (the auth-hold model and this
// resolution chain are Claude's), and a config dir that does not resolve to an
// absolute path — which happens for every session at once when HOME cannot be
// resolved, since the chain's last resort is filepath.Join(home, ".claude") and
// degrades to a bare relative ".claude". That last one is the dangerous one: if
// an empty resolution were allowed to be a key, one unresolvable HOME would
// silently merge an entire fleet into a single fabricated "credential" and the
// aggregation would confidently report one dead token for what might be twelve.
//
// The unknown bucket's escalation says so out loud: it reports the sessions as
// NOT known to share a credential, so an operator never reads it as "one
// re-login fixes these".

// UnknownCredentialKey is the grouping key for held sessions whose credential
// could not be attributed. It is a real, distinct bucket — never a default one
// that unattributable sessions get quietly swept into.
const UnknownCredentialKey = "unknown"

// credentialKeyPrefix namespaces the store-derived key so a key can never
// collide with UnknownCredentialKey, whatever a path looks like.
const credentialKeyPrefix = "store:"

// localHostScope is the key scope for a session running on this machine. It is
// spelled out rather than left as the empty string so a local store and a
// malformed/blank SSH destination can never produce the same key.
const localHostScope = "local"

// sshHostScopePrefix namespaces an SSH destination inside the key, so a host
// literally named "local" cannot collide with localHostScope.
const sshHostScopePrefix = "ssh:"

// CredentialRef identifies WHICH credential store a set of held sessions runs
// under.
//
// Every field is an identifier or a path. Nothing here is, or is derived from,
// token material.
type CredentialRef struct {
	// Key is the stable grouping key. Two sessions with equal Keys share one
	// credential store; UnknownCredentialKey means "not attributable".
	Key string
	// ConfigDir is the resolved credential directory ("" when unattributed).
	ConfigDir string
	// Host is the SSH destination the store lives on, or "" for a store on this
	// machine. Callers must use HostLabel rather than branching on "" directly.
	Host string
	// Attributed is false for the unknown bucket. Callers must branch on this
	// rather than on ConfigDir being non-empty.
	Attributed bool
}

// HostLabel names the machine the credential store lives on.
func (c CredentialRef) HostLabel() string {
	if strings.TrimSpace(c.Host) == "" {
		return localHostScope
	}
	return c.Host
}

// IsRemote reports whether this store lives on another machine, which is what
// makes "re-authenticate once" NOT reach it from here.
func (c CredentialRef) IsRemote() bool { return strings.TrimSpace(c.Host) != "" }

// hostScope renders the key's host component.
//
// An SSH destination is used verbatim after trimming — deliberately NOT
// lowercased or otherwise canonicalised. "user@box" and "other@box" are
// different home directories and so different stores, and DNS-style case folding
// on the host half would risk merging two stores to save an operator one line.
// Splitting costs an extra escalation; merging costs a fleet.
func hostScope(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return localHostScope
	}
	return sshHostScopePrefix + host
}

// unknownCredential returns the unattributed bucket's identity.
func unknownCredential() CredentialRef {
	return CredentialRef{Key: UnknownCredentialKey}
}

// HeldSession is one auth-held session as it appears inside a group.
//
// Reason is the AuthHoldRecord reason (auth_banner_live | auth_death), or "" when
// the caller had no record to read — the recovery sweep observes a 401 boot
// before any sidecar is guaranteed to exist.
type HeldSession struct {
	ID      string
	Title   string
	Account string
	Reason  string
}

// CredentialGroup is every held session behind one credential: the unit an
// operator actually acts on, because one re-login clears the whole group.
type CredentialGroup struct {
	Credential CredentialRef
	// Accounts are the distinct non-empty account slot names seen in this
	// group, sorted. Usually one; more than one means several slots point at
	// the same config dir. Empty means no session named an account.
	Accounts []string
	Sessions []HeldSession
	// Recovered means a LATER observation on this same credential authenticated
	// successfully, so it is not dead after all. Only a sweep can set this (it
	// is the only caller that watches a credential over time); the
	// currently-held query always leaves it false, because a session whose
	// credential recovered is not held any more.
	Recovered bool
}

// Label names the credential store the way an operator would have to refer to
// it.
//
// A remote store names its host, because that is the difference between an
// operator running /login here and an operator needing a shell on another
// machine. A local store keeps the pre-existing wording so the common case reads
// exactly as it did.
func (g CredentialGroup) Label() string {
	if !g.Credential.Attributed {
		return "an unattributable credential"
	}
	where := ""
	if g.Credential.IsRemote() {
		where = fmt.Sprintf(" on ssh://%s", g.Credential.Host)
	}
	if len(g.Accounts) > 0 {
		return fmt.Sprintf("account %s%s (%s)", strings.Join(g.Accounts, ", "), where, g.Credential.ConfigDir)
	}
	return fmt.Sprintf("credential%s %s", where, g.Credential.ConfigDir)
}

// Escalation is the ONE operator-facing line this group replaces N per-session
// ones with.
//
// The unattributed bucket gets a deliberately different sentence: it must not
// read as "one re-login fixes these", because nothing here established that its
// sessions share anything at all.
func (g CredentialGroup) Escalation() string {
	n := len(g.Sessions)
	// Checked before the attributed/unattributed split: telling an operator to
	// re-authenticate a credential that just proved it works is the one thing
	// this report must never do.
	if g.Recovered {
		return fmt.Sprintf(
			"auth-401: %d session(s) failed on %s, but a later boot on that same credential authenticated — it is NOT dead; retry those sessions rather than re-authenticating (%s)",
			n, g.Label(), g.sessionList())
	}
	if !g.Credential.Attributed {
		return fmt.Sprintf(
			"auth-401: %d session(s) held on %s — these are NOT known to share one credential, so re-authenticating one may not recover the others; check them individually (%s)",
			n, g.Label(), g.sessionList())
	}
	// A remote store cannot be repaired from here. Saying "re-authenticate that
	// credential once" without naming where would be the same false promise the
	// host dimension exists to prevent.
	if g.Credential.IsRemote() {
		return fmt.Sprintf(
			"auth-401: %d session(s) held on %s — that credential store lives on %s, so re-authenticating locally will NOT reach it; re-authenticate on that host and every one of them can recover (%s)",
			n, g.Label(), g.Credential.Host, g.sessionList())
	}
	return fmt.Sprintf(
		"auth-401: %d session(s) held on %s — re-authenticate that credential once and every one of them can recover (%s)",
		n, g.Label(), g.sessionList())
}

// sessionList renders the member titles in group order.
func (g CredentialGroup) sessionList() string {
	titles := make([]string, 0, len(g.Sessions))
	for _, s := range g.Sessions {
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = s.ID
		}
		titles = append(titles, title)
	}
	return strings.Join(titles, ", ")
}

// AuthCredentialSummary is the whole credential-level view: the query the issue
// asks for, answerable without re-deriving anything from per-session substates.
type AuthCredentialSummary struct {
	// Groups are attributed credentials first (ordered by key), the unknown
	// bucket last when present.
	Groups []CredentialGroup
	// Held is how many sessions are auth-held in total.
	Held int
	// Credentials is how many DISTINCT credentials are attributed AND still
	// look dead. Two exclusions, both deliberate: the unknown bucket (an
	// unknown number of credentials is not one credential) and any credential
	// that recovered mid-sweep. This is the number an operator reads as "how
	// many re-logins am I facing", so anything it counts must actually need one.
	Credentials int
	// Unattributed is how many held sessions landed in the unknown bucket.
	Unattributed int
	// Recovered is how many attributed credentials failed and then
	// authenticated again during the same sweep.
	Recovered int
}

// Escalations returns one line per credential — the whole point of #1816.
func (s AuthCredentialSummary) Escalations() []string {
	lines := make([]string, 0, len(s.Groups))
	for _, g := range s.Groups {
		lines = append(lines, g.Escalation())
	}
	return lines
}

// Format renders the human-readable credential view.
func (s AuthCredentialSummary) Format() string {
	var b strings.Builder
	if s.Held == 0 {
		b.WriteString("Auth credentials: no sessions are auth-held.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Auth credentials: %d held session(s) behind %d dead credential(s)",
		s.Held, s.Credentials)
	if s.Recovered > 0 {
		fmt.Fprintf(&b, " plus %d that recovered mid-sweep", s.Recovered)
	}
	if s.Unattributed > 0 {
		fmt.Fprintf(&b, " plus %d session(s) whose credential could not be attributed", s.Unattributed)
	}
	b.WriteString("\n")
	for _, g := range s.Groups {
		fmt.Fprintf(&b, "  - %s\n", g.Escalation())
	}
	return b.String()
}

// AuthCredentialGrouper answers "which credentials are dead, and who is held
// behind each one" for a set of sessions.
//
// Both side effects are injectable function fields, matching Recoverer's style
// in this package: the tests drive the real grouping logic without a sidecar
// file, a config.toml, or a tmux server.
type AuthCredentialGrouper struct {
	// Group, when set, restricts the view to that group path and its
	// descendants — the SAME contract as Detector.Group, deliberately using the
	// same inGroup predicate rather than a second copy of the rule.
	//
	// Without this, `fleet status --group X --group-by-credential` answered
	// about the whole fleet while claiming to answer about X: wrong, and most
	// confusing at exactly the moment an operator is narrowing down a problem.
	// (`fleet recover` needs no equivalent: its sweep only ever hands the gate
	// candidates the Detector already filtered.)
	Group string

	// Hold returns a session's auth hold, or nil when it is not currently held.
	// nil uses authHoldOf, which asks the durable per-session record (#1743)
	// through IsAuthHeld/AuthHold so this view and the automatic boot paths
	// always agree on exactly which sessions are held.
	Hold func(*session.Instance) *session.AuthHoldRecord
	// ConfigDir resolves a session's credential directory. nil uses
	// session.GetClaudeConfigDirForInstance — the single source of truth for
	// the priority chain (#881), never a re-implementation of it.
	ConfigDir func(*session.Instance) string
}

// NewAuthCredentialGrouper returns a grouper wired to the real durable hold and
// the real config-dir chain.
func NewAuthCredentialGrouper() *AuthCredentialGrouper {
	return &AuthCredentialGrouper{}
}

// authHoldOf is the default Hold seam: held only while the session is not
// healthy, which is exactly the condition the boot paths gate on.
func authHoldOf(inst *session.Instance) *session.AuthHoldRecord {
	if inst == nil {
		return nil
	}
	if held, _ := inst.IsAuthHeld(); !held {
		return nil
	}
	return inst.AuthHold()
}

func (g *AuthCredentialGrouper) hold(inst *session.Instance) *session.AuthHoldRecord {
	if g == nil || g.Hold == nil {
		return authHoldOf(inst)
	}
	return g.Hold(inst)
}

// inScope applies the --group filter. A nil instance is out of scope only when a
// filter is set, so the unfiltered view keeps reaching Identify's nil handling.
func (g *AuthCredentialGrouper) inScope(inst *session.Instance) bool {
	if g == nil || g.Group == "" {
		return true
	}
	if inst == nil {
		return false
	}
	return inGroup(inst.GroupPath, g.Group)
}

func (g *AuthCredentialGrouper) configDir(inst *session.Instance) string {
	if g == nil || g.ConfigDir == nil {
		return session.GetClaudeConfigDirForInstance(inst)
	}
	return g.ConfigDir(inst)
}

// Identify returns the credential identity of one session, attributed or not.
//
// Exported because the identity rule is the reviewable part of this change: a
// caller (or a test) must be able to ask "what does agent-deck think this
// session's credential is" without going through a whole grouping pass.
func (g *AuthCredentialGrouper) Identify(inst *session.Instance) CredentialRef {
	if inst == nil {
		return unknownCredential()
	}
	// The auth-hold model and the config-dir chain below are both Claude's.
	// Another tool's session may genuinely be held one day; guessing a Claude
	// credential for it would be attribution by wishful thinking.
	//
	// Asked through session.IsClaudeCompatible rather than by comparing the tool
	// name to the literal "claude": a custom tool that wraps the Claude CLI (a
	// path, an env-prefixed command, or an explicit compatible_with = "claude")
	// reads the SAME credential file, so a literal comparison silently drops
	// every aliased session out of the aggregation it belongs in. Matching a
	// literal name where a predicate already answers the question is the same
	// defect class as #1923.
	if !session.IsClaudeCompatible(inst.Tool) {
		return unknownCredential()
	}
	dir := strings.TrimSpace(g.configDir(inst))
	// An unresolvable HOME makes the chain's last resort degrade to a bare
	// relative ".claude" for EVERY session at once. Requiring an absolute path
	// is what stops that from merging a whole fleet into one fabricated
	// credential.
	if dir == "" || !filepath.IsAbs(dir) {
		return unknownCredential()
	}
	dir = filepath.Clean(dir)
	// A remote session's store lives on another machine; see the package doc for
	// why the host is part of the identity and not a display detail.
	host := strings.TrimSpace(inst.SSHHost)
	return CredentialRef{
		Key:        credentialKeyPrefix + hostScope(host) + "|" + dir,
		ConfigDir:  dir,
		Host:       host,
		Attributed: true,
	}
}

// Summarize returns the credential-level view of every auth-held session in
// instances, honouring the Group filter. It reads state and writes nothing: no
// sidecar, no registry row, no log line, and nothing leaves the machine.
//
// Named Summarize rather than Group because Group is the filter field above; a
// method and a field cannot share a name, and the field matches Detector.Group
// so the two scoping knobs read identically at their call sites.
func (g *AuthCredentialGrouper) Summarize(instances []*session.Instance) AuthCredentialSummary {
	acc := newCredAccumulator(g.Identify)
	for _, inst := range instances {
		if !g.inScope(inst) {
			continue
		}
		rec := g.hold(inst)
		if rec == nil {
			continue
		}
		acc.add(inst, HeldSession{
			ID:      instanceID(inst),
			Title:   instanceTitle(inst),
			Account: instanceAccount(inst),
			Reason:  rec.Reason,
		})
	}
	return acc.summary()
}

// credAccumulator collects held sessions into credential groups.
//
// Shared deliberately: AuthCredentialGrouper.Group (the query) and
// SubstateAuthGate.Observe (the sweep) both build their groups here, so there is
// exactly one definition of "one credential" and one ordering, and the two
// surfaces cannot disagree.
type credAccumulator struct {
	ident  func(*session.Instance) CredentialRef
	order  []string
	groups map[string]*CredentialGroup
	held   int
}

func newCredAccumulator(ident func(*session.Instance) CredentialRef) *credAccumulator {
	return &credAccumulator{
		ident:  ident,
		groups: make(map[string]*CredentialGroup),
	}
}

func (a *credAccumulator) add(inst *session.Instance, s HeldSession) {
	ref := a.ident(inst)
	g, ok := a.groups[ref.Key]
	if !ok {
		g = &CredentialGroup{Credential: ref}
		a.groups[ref.Key] = g
		a.order = append(a.order, ref.Key)
	}
	g.Sessions = append(g.Sessions, s)
	if acct := strings.TrimSpace(s.Account); acct != "" && !containsString(g.Accounts, acct) {
		g.Accounts = append(g.Accounts, acct)
		sort.Strings(g.Accounts)
	}
	a.held++
}

// summary is summaryWithRecovered for callers that never watch a credential
// over time (the currently-held query).
func (a *credAccumulator) summary() AuthCredentialSummary {
	return a.summaryWithRecovered(nil)
}

// summaryWithRecovered orders the groups deterministically: attributed
// credentials by key, then the unknown bucket. Unknown goes LAST so a reader who
// stops at the first line reads a real credential, and so the bucket that says
// "these may not be related" is never mistaken for the headline.
//
// recovered names credentials that authenticated after their failures; they stay
// in the output (the operator still has sessions to retry) but are not counted
// as dead.
func (a *credAccumulator) summaryWithRecovered(recovered map[string]bool) AuthCredentialSummary {
	keys := make([]string, 0, len(a.order))
	for _, k := range a.order {
		if k != UnknownCredentialKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	out := AuthCredentialSummary{Held: a.held}
	out.Groups = make([]CredentialGroup, 0, len(a.order))
	for _, k := range keys {
		g := *a.groups[k]
		if recovered[k] {
			g.Recovered = true
			out.Recovered++
		} else {
			out.Credentials++
		}
		out.Groups = append(out.Groups, g)
	}
	if unknown, ok := a.groups[UnknownCredentialKey]; ok {
		out.Unattributed = len(unknown.Sessions)
		out.Groups = append(out.Groups, *unknown)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func instanceID(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	return inst.ID
}

func instanceTitle(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	return inst.Title
}

func instanceAccount(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	return strings.TrimSpace(inst.Account)
}
