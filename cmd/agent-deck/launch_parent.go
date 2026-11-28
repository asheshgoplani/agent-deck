package main

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// launchParentError is what selectLaunchParent returns instead of exiting, so
// each call site (launch, add, the capacity pre-check) keeps its own surface.
// UnresolvedID is set only for a caller id that resolves to nothing.
type launchParentError struct {
	Code         string
	Message      string
	UnresolvedID string
}

func (e *launchParentError) Error() string { return e.Message }

// launchParentErrorParts splits a selector error into message and error code.
func launchParentErrorParts(err error) (message, code string) {
	var lpe *launchParentError
	if errors.As(err, &lpe) {
		return lpe.Message, lpe.Code
	}
	return err.Error(), ErrCodeInvalidOperation
}

// selectLaunchParent picks the session a new session is linked to. It is the
// one place launch, add and the capacity pre-check decide this, so they cannot
// drift apart again.
//
// A parent the user names (explicit) is used as is when it is top-level, and
// refused with the single-level error when it is a sub-session. A parent
// agent-deck picks itself (the launching session) is used as is when it is
// top-level. When it is a sub-session, the result depends on
// nestUnderParent ([launch] nest_under_parent, off by default):
//
//   - off: the new session starts top-level, with no note, and launchedBy is
//     the calling sub-session; --no-parent is silent and does not look for a
//     caller at all.
//   - on: the sub-session's own parent is used instead, with a one-line note,
//     and launchedBy is the calling sub-session. One hop, never a walk; a
//     parent that is itself a sub-session is an error naming both ids, and a
//     parent that no longer exists starts the session top-level with a note,
//     as --no-parent does. From a sub-session, --no-parent's note says where
//     the session would have landed.
//
// launchedBy is set exactly when the caller is a sub-session that launch
// could not use as the parent (issue #3165). The new session reports to it
// (Instance.ReportsTo): its records go to the session that launched it, not
// to the tree parent or nowhere. noParent never sets it: --no-parent asks for
// a peer, and a peer reports to nobody.
//
// noParent means top-level and is honoured from every caller. A caller id
// that resolves to nothing is an error, except with noParent.
func selectLaunchParent(explicit string, noParent, nestUnderParent bool, instances []*session.Instance) (parent, launchedBy *session.Instance, note string, err error) {
	if explicit != "" {
		named, message, _ := ResolveSession(explicit, instances)
		if named == nil {
			return nil, nil, "", &launchParentError{Code: ErrCodeNotFound, Message: message}
		}
		if named.IsSubSession() {
			return nil, nil, "", &launchParentError{Code: ErrCodeInvalidOperation, Message: "cannot create sub-session of a sub-session (single level only)"}
		}
		return named, nil, "", nil
	}
	if noParent && !nestUnderParent {
		return nil, nil, "", nil
	}

	caller, unresolved := resolveAutoParentInstanceChecked(instances)
	if noParent {
		if caller == nil || !caller.IsSubSession() {
			return nil, nil, "", nil
		}
		grandparent := findInstanceByID(instances, caller.ParentSessionID)
		if grandparent == nil || grandparent.IsSubSession() {
			return nil, nil, "", nil
		}
		return nil, nil, fmt.Sprintf("--no-parent given from sub-session %s; started top-level instead of under its parent %s", caller.Title, grandparent.Title), nil
	}
	if caller == nil {
		if unresolved != "" {
			return nil, nil, "", &launchParentError{
				Code:         ErrCodeNotFound,
				UnresolvedID: unresolved,
				Message: fmt.Sprintf("automatic parent %q could not be resolved; use --parent with a valid session or --no-parent for an intentional top-level session",
					unresolved),
			}
		}
		return nil, nil, "", nil
	}
	if !caller.IsSubSession() {
		return caller, nil, "", nil
	}
	if !nestUnderParent {
		return nil, caller, "", nil
	}
	return underParent(caller, instances)
}

// underParent returns the parent of sub-session inst, with inst as the
// launcher, under the one-hop rule. A parent that no longer exists still
// returns inst as the launcher: the session starts top-level and reports to
// inst.
func underParent(inst *session.Instance, instances []*session.Instance) (parent, launchedBy *session.Instance, note string, err error) {
	parent = findInstanceByID(instances, inst.ParentSessionID)
	if parent == nil {
		return nil, inst, fmt.Sprintf("parent %s of sub-session %s (%s) does not exist; started top-level, reporting to %s", inst.ParentSessionID, inst.Title, inst.ID, inst.Title), nil
	}
	if parent.IsSubSession() {
		return nil, nil, "", &launchParentError{
			Code:    ErrCodeInvalidOperation,
			Message: fmt.Sprintf("cannot attach to its parent: %s, the parent of sub-session %s, is itself a sub-session (single level only)", parent.ID, inst.ID),
		}
	}
	return parent, inst, fmt.Sprintf("parent %s is a sub-session; linked under its parent %s", inst.Title, parent.Title), nil
}

// launchNestUnderParent reports whether [launch] nest_under_parent is on. A
// config that cannot be read counts as off, the v1.16.23 behaviour.
func launchNestUnderParent() bool {
	cfg, err := session.LoadUserConfig()
	return err == nil && cfg != nil && cfg.Launch.NestUnderParent
}

func findInstanceByID(instances []*session.Instance, id string) *session.Instance {
	for _, inst := range instances {
		if inst.ID == id {
			return inst
		}
	}
	return nil
}

// reportsToLauncher is the launcher a new session reports to (issue #3165):
// launchedBy when it is a Claude-compatible session, else nil. Only those
// drain an inbox at prompt time and can be woken; a shell sub-session is a
// person at a prompt, and another harness would hold records nobody reads,
// so those launches keep the old routing (tree parent or none).
func reportsToLauncher(launchedBy *session.Instance) *session.Instance {
	if launchedBy == nil || !session.IsClaudeCompatible(launchedBy.Tool) {
		return nil
	}
	return launchedBy
}

// launchReportTarget is the session a new session's records go to: the
// launcher when it reports to one, else the tree parent. A kickoff is tagged
// only when the caller is this session.
func launchReportTarget(parent, launchedBy *session.Instance) *session.Instance {
	if r := reportsToLauncher(launchedBy); r != nil {
		return r
	}
	return parent
}
