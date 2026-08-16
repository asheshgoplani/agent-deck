package session

import "strings"

// Issue #1948 (review round 2, blocking) — pulled records must carry an
// identity that is unique ACROSS HOSTS.
//
// Everything downstream of the inbox keys a record's identity on
// ChildSessionID alone: collapseLastWins reduces to one record per child id,
// TurnFingerprint is child + turn signal, EventFingerprint starts with the
// child id, and the sweeps (SweepInboxByTuple, rm_sweep, the dead-letter store)
// address records by child. That was sound while child ids never left the
// machine that minted them.
//
// This feature is what makes them cross. And child ids are NOT globally unique:
// `run-task --child <ID>` accepts any caller-chosen string, so two boxes running
// the same named task — say a fleet where every host runs `--child nightly-build`
// — mint the same id deterministically. Pulled into one inbox, the two records
// are indistinguishable: the collapse keeps one, the consumed-turn ledger marks
// the other's turn already consumed, and the conductor is told "1 new" for a
// record it will never see. A silent loss, on the feature's headline use case.
//
// Namespacing at ingest fixes every one of those keys at once, because they all
// derive from the child id. It is also the spelling the rest of the repo already
// uses for a remote session's identity: the TUI addresses one as
// "remote:<remote>:<session-id>" (internal/ui/home.go), and `agent-deck remote
// add` rejects a colon in a remote name precisely so that parses
// (isValidRemoteName). A conductor reading `boxb:nightly-build` out of its inbox
// can therefore act on it directly — `agent-deck remote attach boxb nightly-build`.
//
// The alternative — teaching the collapse key and TurnFingerprint about
// SourceRemote — would leave every OTHER child-keyed rule still colliding.

// remoteScopeSeparator is the ':' of `<remote>:<child>`. Remote names cannot
// contain it (isValidRemoteName), so the split is unambiguous.
const remoteScopeSeparator = ":"

// RemoteScopedChildID returns the child id a pulled record is stored under
// locally: `<remote>:<child>`. An empty remote (or child) returns the child
// unchanged — a record with no provenance is left exactly as it was rather than
// given a misleading prefix.
func RemoteScopedChildID(remote, child string) string {
	r := strings.TrimSpace(remote)
	c := strings.TrimSpace(child)
	if r == "" || c == "" {
		return c
	}
	// Already scoped (a re-ingest of a record this conductor stored): leave it,
	// so draining twice cannot produce `boxb:boxb:worker`.
	if strings.HasPrefix(c, r+remoteScopeSeparator) {
		return c
	}
	return r + remoteScopeSeparator + c
}

// SplitRemoteScopedChildID reverses RemoteScopedChildID. ok is false for a
// plain local child id, whose owner is this machine.
func SplitRemoteScopedChildID(id string) (remote, child string, ok bool) {
	r, c, found := strings.Cut(strings.TrimSpace(id), remoteScopeSeparator)
	if !found || strings.TrimSpace(r) == "" || strings.TrimSpace(c) == "" {
		return "", strings.TrimSpace(id), false
	}
	return r, c, true
}
