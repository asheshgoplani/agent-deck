package session

import (
	"log/slog"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
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
//
// 16 hex characters (the UUID's first two groups, ~64 bits) is the floor. The
// obvious 8 is only 32 bits: a neighbouring transcript sharing eight leading
// hex characters would pass, and the whole point of this guard is that a
// neighbouring transcript must not pass. Nothing legitimate abbreviates a
// conversation id below two groups.
const minResumeIDPrefixLen = 16

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
// The suspicion is keyed to the VALUE and never expires on its own: once an id
// has been produced by a disk scan, no later writer can launder it into
// ownership by re-assigning it; only an explicit vouch from a source that
// identifies this session clears it.
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
	if id == "" {
		return ""
	}
	i.claudeSessionIDsFromDiskScanMu.Lock()
	tainted := i.claudeSessionIDsFromDiskScan[id]
	i.claudeSessionIDsFromDiskScanMu.Unlock()
	if tainted {
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
	// Every value here is session-supplied (ids come from disk filenames and
	// tmux/hook payloads; title and path from user input), so each one is
	// sanitized before it reaches the log — message text and structured
	// fields alike. CodeQL go/log-injection.
	safeCandidate := logging.SanitizeValue(candidate)
	safeRecorded := logging.SanitizeValue(i.recordedClaudeSessionID())
	safeReason := logging.SanitizeValue(reason)
	sessionLog.Warn("resume refused: id="+safeCandidate+" reason="+safeReason,
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("title", logging.SanitizeValue(i.Title)),
		slog.String("candidate_session_id", safeCandidate),
		slog.String("recorded_session_id", safeRecorded),
		slog.String("path", logging.SanitizeValue(i.ProjectPath)),
		slog.String("reason", safeReason),
		slog.String("action", "start_fresh"),
	)
}

// canResumeClaudeSession is the single resume-time chokepoint: it answers
// "may this instance be started with `--resume <sessionID>`?".
//
// Nothing outside this file decides that question: callers ask here, and both
// layers below run in order, so a future caller cannot take only half of the
// check by accident.
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
	return conversationIsResumable(inst, sessionID)
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
//
// Order matters: the taint is recorded BEFORE ClaudeSessionID is assigned.
// ClaudeSessionID itself is a plain field with no dedicated lock (pre-existing
// throughout this package; a full lock would need to cover every one of its
// ~20 writer sites, out of scope here), so a concurrent recordedClaudeSessionID()
// read is not excluded by this function. Writing the taint first closes the
// specific window a review finding on #1830 flagged: with the old
// ID-then-taint order, a reader could observe the new id with the taint not
// yet present and treat a disk-scanned id as recorded for one instant.
// Taint-first means any interleaving instead sees either the OLD id (taint
// irrelevant) or the NEW id already tainted -- never the new id un-tainted.
func (i *Instance) adoptDiscoveredClaudeSessionID(uuid string) {
	i.markClaudeSessionIDFromDiskScan(uuid)
	i.ClaudeSessionID = uuid
}

// markClaudeSessionIDFromDiskScan records that uuid came from a disk scan.
func (i *Instance) markClaudeSessionIDFromDiskScan(uuid string) {
	id := strings.TrimSpace(uuid)
	if id == "" {
		return
	}
	i.claudeSessionIDsFromDiskScanMu.Lock()
	defer i.claudeSessionIDsFromDiskScanMu.Unlock()
	if i.claudeSessionIDsFromDiskScan == nil {
		i.claudeSessionIDsFromDiskScan = map[string]bool{}
	}
	i.claudeSessionIDsFromDiskScan[id] = true
}

// markClaudeSessionIDVerified records that the current ClaudeSessionID came
// from a source that identifies THIS session: the tmux environment of its own
// pane, a hook payload it owns, an explicit `--session-id` in its own command,
// or an id this process minted for it.
func (i *Instance) markClaudeSessionIDVerified() {
	id := strings.TrimSpace(i.ClaudeSessionID)
	i.claudeSessionIDsFromDiskScanMu.Lock()
	delete(i.claudeSessionIDsFromDiskScan, id)
	i.claudeSessionIDsFromDiskScanMu.Unlock()
}

// noteClaudeSessionIDFromOwnPane records a WEAK vouch: the id was read back
// from this instance's own tmux pane environment.
//
// Weak because the pane env is downstream of us — agent-deck writes it — so
// treating it as proof would close a laundering loop (scan an id, publish it,
// read it back as "verified"). It therefore records ownership only for a
// value no disk scan ever produced; a suspect value stays suspect until a
// source that is NOT downstream of us vouches for it (an explicit flag, an
// operator's set, a hook payload from the live process, or a minted id).
// Since a suspect id is never published to the pane (SyncSessionIDsToTmux)
// and never spawned, the pane cannot legitimately hold one.
func (i *Instance) noteClaudeSessionIDFromOwnPane() {
	id := strings.TrimSpace(i.ClaudeSessionID)
	if id == "" {
		return
	}
	i.claudeSessionIDsFromDiskScanMu.Lock()
	tainted := i.claudeSessionIDsFromDiskScan[id]
	i.claudeSessionIDsFromDiskScanMu.Unlock()
	if tainted {
		return
	}
	i.markClaudeSessionIDVerified()
}

// NoteClaudeSessionIDFromOwnPane is the exported weak vouch, for the CLI and
// TUI refresh paths that re-read CLAUDE_SESSION_ID from a session's own pane.
func NoteClaudeSessionIDFromOwnPane(inst *Instance) {
	if inst == nil {
		return
	}
	inst.noteClaudeSessionIDFromOwnPane()
}

// MarkClaudeSessionIDVerified is the exported form, for the CLI and TUI
// writers that assign an id from a source identifying THIS session (an
// operator's explicit --resume-session / --session-id, a picked conversation,
// an id the command just minted). Calling it is what clears an earlier
// disk-scan suspicion on that value.
func MarkClaudeSessionIDVerified(inst *Instance) {
	if inst == nil {
		return
	}
	inst.markClaudeSessionIDVerified()
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
