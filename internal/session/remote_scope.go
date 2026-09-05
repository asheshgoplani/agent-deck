package session

import "strings"

// Issue #1948 (review round 2, blocking) — pulled records must carry an
// identity that is unique ACROSS HOSTS.
//
// SourceRemote is the authoritative structured provenance used by inbox
// identity rules. This helper supplies the human-facing `<remote>:<child>`
// spelling; it must not infer that an arbitrary caller-chosen child prefix is
// already scoped.
//
// This feature is what makes them cross. And child ids are NOT globally unique:
// `run-task --child <ID>` accepts any caller-chosen string, so two boxes running
// the same named task — say a fleet where every host runs `--child nightly-build`
// — mint the same id deterministically. Pulled into one inbox, the two records
// are indistinguishable: the collapse keeps one, the consumed-turn ledger marks
// the other's turn already consumed, and the conductor is told "1 new" for a
// record it will never see. A silent loss, on the feature's headline use case.
//
// Namespacing is also the spelling the rest of the repo already
// uses for a remote session's identity: the TUI addresses one as
// "remote:<remote>:<session-id>" (internal/ui/home.go), and `agent-deck remote
// add` rejects a colon in a remote name precisely so that parses
// (isValidRemoteName). A conductor reading `boxb:nightly-build` out of its inbox
// can therefore act on it directly — `agent-deck remote attach boxb nightly-build`.
//
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
