package session

import (
	"strings"
)

// Location answers "where does this session actually run?".
//
// It exists because Instance.ProjectPath is NOT that answer for an --ssh
// session: `add --ssh` stores a LOCAL placeholder there (defaulting to the
// controller's working directory) while the real location lives in SSHHost
// plus SSHRemotePath. Comparing ProjectPath strings therefore reports every
// remote session registered from one local directory as co-located with every
// other one — and with any local session sitting at that directory. That single
// mistake is the root cause behind issues #1850, #1851, #1852 and #1853.
//
// The rule this type encodes: a local location and a remote location are never
// equal, and two remote locations are equal only when BOTH the host and the
// canonical remote path match.
//
// It lives in package session, not in cmd/agent-deck, because identity and
// EXECUTION have to agree. The previous attempt at this epic put the type in
// the CLI, folded "~" into "" for identity, and left wrapForSSH emitting
// `cd '~'` — which does not expand to $HOME — so two spellings that compared
// equal ran in different directories. CanonicalRemotePath and RemoteCDPrefix
// below are the single rule both sides use.
type Location struct {
	// Host is the SSH destination ("user@host"), empty for a local session.
	Host string
	// Path is the canonical remote path for a remote session and the project
	// path for a local one (see CanonicalRemotePath / normalizeLocalPath).
	Path string
}

// normalizeLocalPath strips trailing slashes so "/srv/app", "/srv/app/" and
// "/srv/app//" compare equal, preserving root and the empty (unspecified) path.
//
// Every trailing slash goes, not just one: Location is compared with ==, so any
// spelling this function leaves behind is a location of its own. A path that
// kept one would silently pass the duplicate-title check and then fail to
// resolve depending on how the user typed it.
func normalizeLocalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	trimmed := strings.TrimRight(p, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// CanonicalRemotePath is the ONE spelling of a remote directory that identity
// comparison and command execution both use.
//
// The remote default directory has three spellings that all name the same
// place, because `ssh host <cmd>` runs <cmd> from the remote user's home:
//
//	""    — `add --ssh alice@host` with no --remote-path; no `cd` is emitted
//	"~"   — what Location.String() prints, so it is what a user types back
//	"~/"  — the same with a trailing slash
//
// All three canonicalize to "".
//
// A RELATIVE remote path is resolved the same way, for the same reason: `ssh
// host <cmd>` starts in the remote home, so `--remote-path work` runs in
// $HOME/work — the identical directory `--remote-path ~/work` names. They are
// therefore ONE location and canonicalize to the ~-rooted form.
//
// Folding relative paths here rather than teaching ParseLocation to accept them
// is what keeps Location.String() parseable (review round 1, finding F3):
// String() used to render a relative path as "host:work", which ParseLocation
// rejected, so an ambiguity message printed an identifier that then resolved to
// NOT_FOUND. ParseLocation stays strict — accepting "host:relative" there would
// let the explicit-location syntax shadow ordinary titles, which is the hazard
// the strictness exists to prevent.
//
// Everything else keeps its absolute form with trailing slashes stripped.
//
// This is the identity half of the rule. RemoteCDPrefix is the execution half,
// and they are tested against each other (see
// TestRemoteCDPrefix_IdentityAndExecutionAgree).
func CanonicalRemotePath(p string) string {
	p = normalizeLocalPath(p)
	if p == "~" || p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return p
	}
	// Relative to the remote home. Strip a leading "./" so "./work", "work" and
	// "~/work" are one location, and fold a bare "." onto the home itself.
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	if p == "" || p == "." {
		return ""
	}
	return "~/" + p
}

// LocalLocation builds the location of a session that runs on this machine.
func LocalLocation(path string) Location {
	return Location{Path: normalizeLocalPath(path)}
}

// RemoteLocation builds the location of an --ssh session. An empty (canonical)
// remote path means "the remote user's home directory" — still a distinct place
// from any local path, so it never collapses onto the local form.
func RemoteLocation(host, remotePath string) Location {
	host = strings.TrimSpace(host)
	if host == "" {
		return LocalLocation(remotePath)
	}
	return Location{Host: host, Path: CanonicalRemotePath(remotePath)}
}

// LocationOf returns where inst actually runs, consulting SSHHost/SSHRemotePath
// instead of trusting ProjectPath.
func LocationOf(inst *Instance) Location {
	if inst == nil {
		return Location{}
	}
	if inst.IsSSH() {
		return RemoteLocation(inst.SSHHost, inst.SSHRemotePath)
	}
	return LocalLocation(inst.ProjectPath)
}

// IsLocal reports whether this location is on the controller machine — i.e.
// whether its path is a real path in this filesystem.
func (l Location) IsLocal() bool { return l.Host == "" }

// String renders the location for humans. A message about a remote session must
// name the host and remote directory; naming the local placeholder (as the
// pre-fix duplicate/rename messages did) points the user at a directory that has
// nothing to do with the collision.
//
// The rendered form round-trips: ParseLocation(l.String()) == l for every remote
// location, so an ambiguity message hands the user an identifier that resolves.
// That is why the empty remote path prints as "~" (and why CanonicalRemotePath
// folds "~" back to ""), and why a relative remote path is canonicalized to its
// ~-rooted form before it ever reaches String(). The property is pinned by
// TestLocationString_RoundTripsThroughParseLocation over every path shape.
//
// A LOCAL location renders as the bare path, which ParseLocation deliberately
// does not accept — bare paths are answered by ResolveSession's path branch.
func (l Location) String() string {
	if l.IsLocal() {
		return l.Path
	}
	if l.Path == "" {
		return l.Host + ":~"
	}
	return l.Host + ":" + l.Path
}

// ParseLocation interprets a user-typed location. `[user@]host:/path` and
// `[user@]host:~[/path]` address a remote session explicitly; anything else is
// not a location identifier (callers treat it as a bare path).
//
// A Windows-style drive letter ("C:\src") is not a host:path form, and neither
// is a relative "foo:bar" — the remote form requires an absolute or ~-rooted
// remote path.
func ParseLocation(identifier string) (Location, bool) {
	idx := strings.Index(identifier, ":")
	if idx <= 0 {
		return Location{}, false
	}
	host, remotePath := identifier[:idx], identifier[idx+1:]
	if host == "" || strings.ContainsAny(host, "/\\ ") {
		return Location{}, false
	}
	if !strings.HasPrefix(remotePath, "/") && !strings.HasPrefix(remotePath, "~") {
		return Location{}, false
	}
	return RemoteLocation(host, remotePath), true
}

// RemoteCDPrefix renders the `cd <dir> && ` prefix that puts the remote command
// in remotePath, or "" when no `cd` is needed.
//
// This is the EXECUTION half of the rule CanonicalRemotePath states for
// identity, and the two must agree or a session runs somewhere other than where
// its identity says it does:
//
//   - canonical "" (i.e. "", "~", "~/") emits nothing. `ssh host <cmd>` already
//     starts <cmd> in the remote user's home, so "no cd" and "cd $HOME" are the
//     same directory — which is exactly why the three spellings are ONE location.
//   - a ~-rooted path keeps its tilde UNQUOTED so the remote shell expands it
//     against the REMOTE user's home. `cd '~/work'` (the old form) is a literal
//     directory named "~" — the round-3 review's blocking finding.
//   - anything else is a literal path and is single-quoted in full.
//
// The result is spliced into the payload that wrapForSSH hands to ssh, which
// quotes the whole program exactly once. Every character except a deliberate
// tilde prefix is inside single quotes, so a remote path containing ;/$() cannot
// inject.
func RemoteCDPrefix(remotePath string) string {
	p := CanonicalRemotePath(remotePath)
	if p == "" {
		return ""
	}
	return "cd " + remoteDirShellExpr(p) + " && "
}

// remoteDirShellExpr quotes a canonical remote path for the remote shell,
// leaving a tilde prefix unquoted so it expands. It is only ever called with a
// non-empty canonical path.
func remoteDirShellExpr(p string) string {
	if !strings.HasPrefix(p, "~") {
		return shellQuote(p)
	}
	// Split "~" or "~user" off the first segment; bash only expands a tilde
	// prefix that is unquoted and at the start of the word.
	prefix, rest := p, ""
	if idx := strings.Index(p, "/"); idx >= 0 {
		prefix, rest = p[:idx], p[idx+1:]
	}
	if !tildePrefixIsShellSafe(prefix) {
		// A username we cannot vouch for: quote the whole thing rather than
		// splice unquoted text into the remote shell. The cd then fails
		// loudly on the remote instead of executing something we did not mean.
		return shellQuote(p)
	}
	if rest == "" {
		return prefix
	}
	return prefix + "/" + shellQuote(rest)
}

// tildePrefixIsShellSafe reports whether prefix is "~" or "~<username>" with a
// username made only of characters that cannot mean anything to a shell.
func tildePrefixIsShellSafe(prefix string) bool {
	if prefix == "~" {
		return true
	}
	if !strings.HasPrefix(prefix, "~") {
		return false
	}
	for _, r := range prefix[1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}
