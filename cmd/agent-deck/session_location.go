package main

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// This file is the CLI half of "a session's identity is WHERE IT RUNS".
//
// The location primitive itself lives in internal/session (session.Location,
// session.CanonicalRemotePath, session.RemoteCDPrefix) because identity and
// EXECUTION have to agree: the canonical spelling of a remote path is the same
// rule that decides which directory the remote command actually `cd`s into. The
// helpers below are the CLI-facing decisions built on top of it — duplicate
// detection, the visible auto-rename, title-conflict refusal, and the
// local-only path queries.

// reloadForRegistrationFn is the seam the registration paths read the instance
// list through, indirected so tests can inject the transient failure this
// function exists to handle.
var reloadForRegistrationFn = defaultReloadForRegistration

// reloadForRegistration re-reads the instance list while the caller holds the
// profile registration lock.
//
// It returns an error rather than a stale list, and every caller must abort on
// it. That is the whole point: the list loaded BEFORE the lock is the snapshot
// the lock exists to invalidate, so continuing with it after a failed re-read
// gives up exactly the atomicity the lock was taken for. Concretely (review
// round 1, finding F2): process A loads the list, process B registers
// (title, location), A takes the lock, A's re-read fails transiently with
// SQLITE_BUSY — and if A proceeds on the pre-lock snapshot it accepts the same
// (title, location) B just took. Worse, `add`'s whole-list SaveWithGroups then
// rewrites the table from that stale slice and erases B's row.
//
// Failing the command is the right outcome: `add`/`launch`/`rename` are cheap to
// retry, and a duplicate registration or a deleted sibling session is not.
func reloadForRegistration(storage *session.Storage) ([]*session.Instance, []*session.GroupData, error) {
	return reloadForRegistrationFn(storage)
}

func defaultReloadForRegistration(storage *session.Storage) ([]*session.Instance, []*session.GroupData, error) {
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		return nil, nil, fmt.Errorf("re-reading sessions under the registration lock: %w", err)
	}
	return instances, groups, nil
}

// locationOf returns where inst actually runs.
func locationOf(inst *session.Instance) session.Location { return session.LocationOf(inst) }

// localLocation builds the location of a session that runs on this machine.
func localLocation(path string) session.Location { return session.LocalLocation(path) }

// remoteLocation builds the location of an --ssh session.
func remoteLocation(host, remotePath string) session.Location {
	return session.RemoteLocation(host, remotePath)
}

// isDuplicateSessionAt reports whether an existing session holds this title at
// this location. Title plus location is what the CLI treats as a session's
// identity (see ResolveSession), so this is the single predicate every
// identity-collision check uses.
func isDuplicateSessionAt(instances []*session.Instance, title string, loc session.Location) (bool, *session.Instance) {
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.Title == title && locationOf(inst) == loc {
			return true, inst
		}
	}
	return false, nil
}

// isDuplicateSession is the local-session form of isDuplicateSessionAt, kept for
// the callers that can only ever register on this machine. Note that a local
// path no longer matches a remote session's placeholder.
func isDuplicateSession(instances []*session.Instance, title, path string) (bool, *session.Instance) {
	return isDuplicateSessionAt(instances, title, localLocation(path))
}

// generateUniqueTitleAt generates a unique title for sessions at one location.
// If "project" already exists there, returns "project (2)", then "project (3)".
// Comparing by location keeps the bump from firing against a remote session that
// merely stores the same local placeholder.
func generateUniqueTitleAt(instances []*session.Instance, baseTitle string, loc session.Location) string {
	titleExists := func(title string) bool {
		exists, _ := isDuplicateSessionAt(instances, title, loc)
		return exists
	}

	if !titleExists(baseTitle) {
		return baseTitle
	}

	for i := 2; i <= 100; i++ { // Cap at 100 to prevent infinite loop
		candidate := fmt.Sprintf("%s (%d)", baseTitle, i)
		if !titleExists(candidate) {
			return candidate
		}
	}

	// Fallback: use timestamp
	return fmt.Sprintf("%s (%d)", baseTitle, time.Now().Unix())
}

// generateUniqueTitle is the local-session form of generateUniqueTitleAt.
func generateUniqueTitle(instances []*session.Instance, baseTitle, path string) string {
	return generateUniqueTitleAt(instances, baseTitle, localLocation(path))
}

// addTitleDecision is what `add` decided about the requested title at the
// location the session will actually run in. It exists so the three outcomes the
// CLI must distinguish — created, created-under-a-different-name, refused — are
// one value the caller renders, rather than three scattered branches with three
// different exit codes (issue #1850).
type addTitleDecision struct {
	// Title is the title to register. Equals the requested title unless
	// RenamedFrom is set.
	Title string
	// Duplicate is the existing session holding the requested title at this
	// location. Non-nil means "refuse": the caller reports ALREADY_EXISTS and
	// exits non-zero, matching `launch`.
	Duplicate *session.Instance
	// RenamedFrom is the requested title when it was auto-bumped, empty
	// otherwise. Two agents on one checkout is a legitimate workflow, so the
	// bump stays — but it must leave a trace.
	RenamedFrom string
	// Location is where the session runs, used for messages.
	Location session.Location
}

// decideAddTitle resolves the requested title against the sessions already
// registered at loc. An explicit -t is never renamed behind the user's back: it
// is either accepted or refused as a duplicate. Without -t the title is a
// derived default, so bumping it is reasonable.
//
// Concurrency: the caller must hold the profile registration lock
// (session.AcquireRegistrationLock) and must have re-read `instances` while
// holding it. This function is pure; the atomicity lives at the call site.
func decideAddTitle(instances []*session.Instance, requested string, loc session.Location, userProvidedTitle bool) addTitleDecision {
	d := addTitleDecision{Title: requested, Location: loc}

	if userProvidedTitle {
		if isDupe, existing := isDuplicateSessionAt(instances, requested, loc); isDupe {
			d.Duplicate = existing
		}
		return d
	}

	if unique := generateUniqueTitleAt(instances, requested, loc); unique != requested {
		d.Title = unique
		d.RenamedFrom = requested
	}
	return d
}

// DuplicateError renders the refusal. The ALREADY_EXISTS code matches `launch`
// and the convention used elsewhere in the CLI, and the message names the
// existing session's id so the user can act on it, plus the location that
// actually collided (host and remote directory for a remote session).
func (d addTitleDecision) DuplicateError() (string, string) {
	if d.Duplicate == nil {
		return "", ""
	}
	return fmt.Sprintf("session already exists: %q at %s (id: %s)",
		d.Duplicate.Title, d.Location.String(), d.Duplicate.ID), ErrCodeAlreadyExists
}

// DuplicateJSONFields are the machine-checkable fields merged into the --json
// error payload. Before #1850 this path ignored --json entirely, so a machine
// consumer got no structured signal at all.
func (d addTitleDecision) DuplicateJSONFields() map[string]interface{} {
	if d.Duplicate == nil {
		return nil
	}
	fields := map[string]interface{}{
		"existing_id":    d.Duplicate.ID,
		"existing_title": d.Duplicate.Title,
		"location":       d.Location.String(),
	}
	if !d.Location.IsLocal() {
		fields["ssh_host"] = d.Location.Host
		fields["ssh_remote_path"] = d.Location.Path
	}
	return fields
}

// RenameWarning renders the trace the silent rename used to lack. Empty when
// nothing was renamed. The caller writes it to stderr and suppresses it under
// --json and -q, so the exit code and stdout contract are unchanged.
func (d addTitleDecision) RenameWarning() string {
	if d.RenamedFrom == "" {
		return ""
	}
	return fmt.Sprintf("Warning: a session titled %q already exists at %s; registering this one as %q instead. Pass -t to choose a title.",
		d.RenamedFrom, d.Location.String(), d.Title)
}

// titleConflictAt returns the OTHER session holding newTitle at inst's location,
// or nil when the rename is free. It is the guard `session set <id> title` was
// missing (#1853): `add` and `launch` both refuse that state, so without it the
// state `add` will not let you create was reachable in one command — and two
// sessions sharing a title at one location are then both unaddressable by title,
// because ResolveSession reports them as ErrCodeAmbiguous.
//
// The check lives at the CLI callers rather than in session.SetField because
// SetField takes a single *Instance with no access to the instance list, and the
// TUI edit dialog has no collision check of its own to inherit from.
func titleConflictAt(instances []*session.Instance, inst *session.Instance, newTitle string) *session.Instance {
	if inst == nil {
		return nil
	}
	loc := locationOf(inst)
	for _, other := range instances {
		if other == nil || other.ID == inst.ID {
			continue
		}
		if other.Title == newTitle && locationOf(other) == loc {
			return other
		}
	}
	return nil
}

// titleConflictError renders the refusal for titleConflictAt, using the same
// ALREADY_EXISTS code `add` and `launch` use and naming the existing session's
// ID so the user can act on it.
func titleConflictError(inst *session.Instance, newTitle string, conflict *session.Instance) (string, string) {
	return fmt.Sprintf("session with title %q already exists at %s (id: %s)",
		newTitle, locationOf(inst).String(), conflict.ID), ErrCodeAlreadyExists
}

// checkTitleConflict is the one decision every rename path makes: it returns the
// (message, code) pair to report when newTitle is already held by ANOTHER
// session at inst's location, and ("", "") when the rename is free.
//
// `add`, `rename` and `session set <id> title` all refuse the same condition, so
// they must report it with the same code — `rename` used to answer
// INVALID_OPERATION where the other two answer ALREADY_EXISTS, forcing a --json
// consumer to special-case one of the three (review finding F8). Renaming a
// session to the title it already holds stays a no-op.
func checkTitleConflict(instances []*session.Instance, inst *session.Instance, newTitle string) (string, string) {
	if inst == nil || newTitle == inst.Title {
		return "", ""
	}
	conflict := titleConflictAt(instances, inst, newTitle)
	if conflict == nil {
		return "", ""
	}
	return titleConflictError(inst, newTitle, conflict)
}

// instanceAtLocationIdentifier reports whether inst runs at the location the
// user named. Matching the location's path (SSHRemotePath for a remote session,
// ProjectPath for a local one) means `agent-deck session /srv/app-a` addresses a
// remote session by the path it actually runs at, and the controller's local
// placeholder no longer matches every remote session at once (#1852 site 1).
func instanceAtLocationIdentifier(inst *session.Instance, identifier string) bool {
	loc := locationOf(inst)
	if want, ok := session.ParseLocation(identifier); ok {
		return loc == want
	}
	return loc.Path == session.LocalLocation(identifier).Path
}

// describeLocations renders one "title (id) at <location>" line per session for
// an ambiguity message. Naming the location — host and remote directory for a
// remote session, not its local placeholder — is what makes the message
// actionable: it is the identifier the user retypes to pick one.
func describeLocations(matches []*session.Instance) []string {
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		id := m.ID
		if len(id) > 12 {
			id = id[:12]
		}
		names = append(names, fmt.Sprintf("%s (%s) at %s", m.Title, id, locationOf(m).String()))
	}
	return names
}

// localSessionPaths returns the set of LOCAL filesystem paths that sessions
// occupy — project paths and worktree paths. A remote session contributes
// nothing: its ProjectPath is a placeholder, not a checkout, so inserting it
// makes a local worktree at that path look occupied and hides a genuinely
// orphaned worktree from cleanup (#1852 sites 2 and 3).
func localSessionPaths(instances []*session.Instance) map[string]bool {
	paths := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if loc := locationOf(inst); loc.IsLocal() && loc.Path != "" {
			paths[loc.Path] = true
		}
		if inst.WorktreePath != "" {
			paths[inst.WorktreePath] = true
		}
	}
	return paths
}

// localSessionsAtPath returns every session that runs locally at path. Callers
// that can only mean a local session (worktree occupancy, `try`, the tmux-cwd
// fallback) must use this rather than comparing ProjectPath, so a remote session
// whose placeholder happens to equal path is never picked up (#1852 sites 4
// and 5).
func localSessionsAtPath(instances []*session.Instance, path string) []*session.Instance {
	want := localLocation(path)
	var matches []*session.Instance
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if locationOf(inst) == want {
			matches = append(matches, inst)
		}
	}
	return matches
}

// localSessionForPaneCwd picks the session that owns a non-agentdeck tmux pane
// whose cwd is path, or nil when the pane belongs to no registered session.
//
// A pane's cwd is a LOCAL path, so only a local session can own it: a remote
// session's ProjectPath is a placeholder that frequently equals the controller's
// working directory, and matching it would attribute this pane to a session
// running on another host (#1852 site 4). That exclusion is the whole fix.
//
// Several LOCAL sessions at one cwd is a different question, and the answer
// stays what it has always been — the first registered one (review finding F7).
// Two agents on one checkout is the workflow #1850's auto-rename exists to
// support, and refusing to self-detect there would take a feature away to buy
// nothing: the caller only reports which session the pane sits in.
func localSessionForPaneCwd(instances []*session.Instance, path string) *session.Instance {
	if matches := localSessionsAtPath(instances, path); len(matches) > 0 {
		return matches[0]
	}
	return nil
}
