package session

import (
	"log/slog"
	"strings"
)

// Resume-time identity guard (#1815).
//
// Incident: a session whose own conversation id had been lost (an account
// switch reported "no conversation to migrate, fresh session") was restarted.
// The restart prelude fell back to disk discovery, which returns the NEWEST
// UUID-named transcript filed under the encoded working directory — not
// necessarily this session's own. The restarted pane therefore came up
// `--resume`ing a transcript belonging to a DIFFERENT session that shared the
// directory, complete with that session's context and authority.
//
// The structural defect: every upstream producer of a session id (tmux env,
// hook payload, explicit --session-id flag, disk discovery, account-switch
// relocation) fed the same field, and the command builders trusted whatever
// was in it. Nothing verified, at the moment the `--resume <id>` string was
// assembled, that the id was this session's OWN.
//
// The guard closes that at the last step, where it cannot be bypassed by
// fixing only some upstream paths:
//
//	recorded id missing  -> refuse, start fresh
//	recorded id mismatch -> refuse, start fresh
//	otherwise            -> the existing conversation-data / project-dir checks decide
//
// Starting fresh loses at most one conversation's history. Resuming another
// session's conversation hands this pane that session's context and its
// authority, which is not a recoverable mistake.
//
// Composition with the foreign-project-dir guard (#1788): that change hardens
// sessionHasConversationData so a jsonl found under an unrelated project
// directory can no longer justify `--resume`. It rejects by LOCATION; this one
// rejects by IDENTITY. They are deliberately layered inside the single
// chokepoint canResumeClaudeSession below (identity first, then
// sessionHasConversationData) rather than left as two partial checks that
// callers might reach independently.

// minResumeIDPrefixLen is the shortest prefix accepted when matching a
// recorded conversation id against a candidate. The Claude CLI resolves
// `--resume` ids by prefix, so an operator-supplied or CLI-echoed prefix is a
// legitimate spelling of the same id — but a prefix short enough to collide
// across unrelated UUIDs is not identity evidence.
const minResumeIDPrefixLen = 8

// claudeSessionIDsMatch reports whether candidate denotes the same
// conversation as recorded. Exact match, or either value being a
// sufficiently long prefix of the other (the CLI accepts id prefixes).
// Comparison is case-insensitive: UUID hex spelling is not significant.
func claudeSessionIDsMatch(recorded, candidate string) bool {
	r := strings.ToLower(strings.TrimSpace(recorded))
	c := strings.ToLower(strings.TrimSpace(candidate))
	if r == "" || c == "" {
		return false
	}
	if r == c {
		return true
	}
	shorter, longer := r, c
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	if len(shorter) < minResumeIDPrefixLen {
		return false
	}
	return strings.HasPrefix(longer, shorter)
}

// resumeIdentityDecision is the outcome of the identity half of the guard.
type resumeIdentityDecision struct {
	Allow  bool
	Reason string
}

// recordedClaudeSessionID returns the conversation id this instance is known
// to OWN, or "" when no such id is recorded.
//
// An id populated by disk discovery is deliberately NOT recorded ownership:
// discovery picks the newest transcript filed under the working directory,
// which in a shared directory is somebody else's. Such an id is usable as a
// hint (it still routes the restart through the resume builder, preserving the
// existing dispatch shape) but must never authorize `--resume`.
func (i *Instance) recordedClaudeSessionID() string {
	if i == nil {
		return ""
	}
	id := strings.TrimSpace(i.ClaudeSessionID)
	if id != "" && id == i.claudeSessionIDUnverifiedFor {
		return ""
	}
	return id
}

// resumeIdentityAllowed is THE identity check. Every path that assembles a
// `--resume` for a Claude conversation routes through it (directly, or via
// canResumeClaudeSession): restart, start, revive/adopt preludes, fork,
// account switch and the TUI session picker.
//
// candidate is the id that is about to be handed to `--resume`.
func (i *Instance) resumeIdentityAllowed(candidate string) resumeIdentityDecision {
	c := strings.TrimSpace(candidate)
	if c == "" {
		return resumeIdentityDecision{Reason: "empty_candidate_id"}
	}
	recorded := i.recordedClaudeSessionID()
	if recorded == "" {
		return resumeIdentityDecision{Reason: "no_recorded_session_id"}
	}
	if !claudeSessionIDsMatch(recorded, c) {
		return resumeIdentityDecision{Reason: "identity_mismatch"}
	}
	return resumeIdentityDecision{Allow: true, Reason: "identity_verified"}
}

// logResumeRefusal emits the loud, grep-stable refusal record. Resuming the
// wrong conversation is silent by nature — the pane simply comes up as some
// other session — so the refusal must be visible in the log even though the
// user-visible effect (a fresh session) looks unremarkable.
func (i *Instance) logResumeRefusal(candidate, reason string) {
	sessionLog.Warn("resume refused: id="+candidate+" reason="+reason,
		slog.String("instance_id", i.ID),
		slog.String("title", i.Title),
		slog.String("candidate_session_id", candidate),
		slog.String("recorded_session_id", i.recordedClaudeSessionID()),
		slog.String("path", i.ProjectPath),
		slog.String("reason", reason),
		slog.String("action", "start_fresh"),
	)
}

// canResumeClaudeSession is the single resume-time chokepoint: it answers
// "may this instance be started with `--resume <sessionID>`?".
//
// Layer 1 (this change, #1815): identity — the id must be this instance's own
// recorded conversation id.
// Layer 2 (sessionHasConversationData, hardened by #1788): the transcript must
// exist AND live in a project directory `claude --resume` can actually reach
// from this instance's working dir.
//
// Callers must not skip straight to sessionHasConversationData when deciding
// between --resume and --session-id; that function also serves bind-quality
// gates (hook/tmux rebinding) where the question is "does this id have any
// conversation data", not "may we resume it".
func canResumeClaudeSession(inst *Instance, sessionID string) bool {
	if inst == nil {
		return false
	}
	if decision := inst.resumeIdentityAllowed(sessionID); !decision.Allow {
		inst.logResumeRefusal(sessionID, decision.Reason)
		return false
	}
	return sessionHasConversationData(inst, sessionID)
}

// ResumeIdentityAllowed exposes the identity half of the chokepoint to
// callers outside this package (the TUI session picker) that must not apply
// the conversation-data heuristics. It logs its own refusals, so a caller
// only needs to act on the boolean; the reason is returned for messaging.
func ResumeIdentityAllowed(inst *Instance, sessionID string) (bool, string) {
	if inst == nil {
		return false, "nil_instance"
	}
	decision := inst.resumeIdentityAllowed(sessionID)
	if !decision.Allow {
		inst.logResumeRefusal(sessionID, decision.Reason)
	}
	return decision.Allow, decision.Reason
}

// NewClaudeSessionUUID mints a fresh conversation id. Used when the guard
// refuses a resume: the refused id belongs to (or may belong to) another
// session, so it must not be reused via `--session-id` either.
func NewClaudeSessionUUID() string { return generateUUID() }

// adoptDiscoveredClaudeSessionID records an id obtained from mtime-based disk
// discovery. The id is marked UNVERIFIED: it is a hint about which transcript
// exists in this directory, never proof of ownership.
func (i *Instance) adoptDiscoveredClaudeSessionID(uuid string) {
	i.ClaudeSessionID = uuid
	i.claudeSessionIDUnverifiedFor = strings.TrimSpace(uuid)
}

// AdoptDiscoveredClaudeSessionID is the exported form for callers outside
// this package that resolve a conversation id by scanning disk (the
// account-switch command, which locates "the newest conversation in this
// project dir" when the session carries no recorded id).
func AdoptDiscoveredClaudeSessionID(inst *Instance, uuid string) {
	if inst == nil {
		return
	}
	inst.adoptDiscoveredClaudeSessionID(uuid)
}

// markClaudeSessionIDVerified records that the current ClaudeSessionID came
// from a source that identifies THIS session: the tmux environment of its own
// pane, a hook payload it owns, an explicit `--session-id` in its own command,
// or an id this process minted for it.
func (i *Instance) markClaudeSessionIDVerified() {
	i.claudeSessionIDUnverifiedFor = ""
}

// replaceRefusedClaudeSessionID mints and records a fresh conversation id
// after the guard refused a resume, so the session starts clean instead of
// claiming an id that may belong to another session (a shared id also makes
// the duplicate sweeper kill one of the pair).
func (i *Instance) replaceRefusedClaudeSessionID() string {
	fresh := NewClaudeSessionUUID()
	i.ClaudeSessionID = fresh
	i.markClaudeSessionIDVerified()
	return fresh
}
