package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Resume-identity guard suite (#1815).
//
// Incident shape being pinned: a session whose own conversation id had been
// lost (an account switch reported "no conversation to migrate, fresh
// session") was restarted. The restart's disk-discovery prelude returned the
// newest transcript filed under the working directory — one belonging to a
// DIFFERENT session — and the restart resumed it. The restarted pane came up
// as a live second instance of that other session, carrying its context and
// its authority.
//
// Contract: at the moment a `--resume` is assembled, the id must match the
// session's OWN recorded conversation id. No recorded id, or a mismatch,
// means start fresh — and the refused id is not reused via --session-id
// either, since it may belong to another session.

// stageConversation writes a conversation jsonl for sessionID under home's
// Claude projects dir for projectPath and returns its path.
func stageConversation(t *testing.T, home, projectPath, sessionID string) string {
	t.Helper()
	dir := claudeProjectDirForTest(t, filepath.Join(home, ".claude"), projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	body := `{"type":"user","sessionId":"` + sessionID + `","text":"hello"}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func newGuardInstance(t *testing.T, home string) *Instance {
	t.Helper()
	projectPath := filepath.Join(home, "shared-project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	inst := NewInstanceWithTool("guard-test", projectPath, "claude")
	inst.ClaudeSessionID = ""
	return inst
}

// TestResumeGuard_DiscoveredForeignTranscriptIsNotResumed is the incident
// itself: session A has NO recorded conversation id, and disk discovery offers
// session B's transcript from the shared working directory. The restart must
// NOT resume B — and must not claim B's id via --session-id either.
func TestResumeGuard_DiscoveredForeignTranscriptIsNotResumed(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const foreignID = "bbbbbbbb-2222-4333-8444-555555555555"
	stageConversation(t, home, inst.ProjectPath, foreignID)

	// Restart() implies the session previously ran.
	inst.ClaudeDetectedAt = time.Now()
	inst.ensureClaudeSessionIDFromDiskForRestart()

	if inst.ClaudeSessionID != foreignID {
		t.Fatalf("precondition: discovery should surface the only transcript on disk; got %q", inst.ClaudeSessionID)
	}
	if got := inst.recordedClaudeSessionID(); got != "" {
		t.Fatalf("a discovered id must not count as a RECORDED (owned) id; recordedClaudeSessionID = %q", got)
	}

	cmd := inst.buildClaudeResumeCommand()

	if strings.Contains(cmd, "--resume") {
		t.Fatalf("#1815: restart must NOT --resume a transcript this session does not own.\ncommand: %s", cmd)
	}
	if strings.Contains(cmd, foreignID) {
		t.Fatalf("#1815: the refused id belongs to another session and must not be reused via --session-id either.\ncommand: %s", cmd)
	}
	if !strings.Contains(cmd, "--session-id ") {
		t.Fatalf("refusal must start a fresh session via --session-id.\ncommand: %s", cmd)
	}
	if inst.ClaudeSessionID == foreignID || inst.ClaudeSessionID == "" {
		t.Fatalf("instance must carry a freshly minted id after refusal; got %q", inst.ClaudeSessionID)
	}
	if inst.recordedClaudeSessionID() != inst.ClaudeSessionID {
		t.Fatalf("the freshly minted id is this session's own and must be recorded as verified")
	}
}

// TestResumeGuard_MissingRecordedUUIDRefuses pins the rule directly: with no
// recorded conversation id, no candidate may be resumed.
func TestResumeGuard_MissingRecordedUUIDRefuses(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const candidate = "cccccccc-3333-4444-8555-666666666666"
	stageConversation(t, home, inst.ProjectPath, candidate)
	inst.adoptDiscoveredClaudeSessionID(candidate)

	if decision := inst.resumeIdentityAllowed(candidate); decision.Allow {
		t.Fatalf("resume must be refused when no recorded conversation id exists (reason=%s)", decision.Reason)
	} else if decision.Reason != "no_recorded_session_id" {
		t.Fatalf("reason = %q, want no_recorded_session_id", decision.Reason)
	}
	if canResumeClaudeSession(inst, candidate) {
		t.Fatal("chokepoint must refuse even though the transcript exists and has conversation data")
	}
}

// TestResumeGuard_IdentityMismatchRefuses covers the other refusal: a recorded
// id exists, but the candidate about to be resumed is a different conversation.
func TestResumeGuard_IdentityMismatchRefuses(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const ownID = "11111111-1111-4111-8111-111111111111"
	const otherID = "99999999-9999-4999-8999-999999999999"
	stageConversation(t, home, inst.ProjectPath, ownID)
	stageConversation(t, home, inst.ProjectPath, otherID)
	inst.ClaudeSessionID = ownID

	if canResumeClaudeSession(inst, otherID) {
		t.Fatal("#1815: a candidate that is not this session's recorded conversation must be refused")
	}
	if !canResumeClaudeSession(inst, ownID) {
		t.Fatal("the session's own conversation must still resume normally")
	}
}

// TestResumeGuard_MatchingIDStillResumes is the no-regression case, including
// the prefix spelling the Claude CLI accepts.
func TestResumeGuard_MatchingIDStillResumes(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const ownID = "abcdef01-2345-4678-8abc-def012345678"
	stageConversation(t, home, inst.ProjectPath, ownID)
	inst.ClaudeSessionID = ownID

	if !canResumeClaudeSession(inst, ownID) {
		t.Fatal("exact id match must resume")
	}
	if decision := inst.resumeIdentityAllowed(ownID[:16]); !decision.Allow {
		t.Fatalf("id PREFIX form must be accepted (the CLI resolves --resume by prefix); reason=%s", decision.Reason)
	}

	cmd := inst.buildClaudeResumeCommand()
	if !strings.Contains(cmd, "--resume "+ownID) {
		t.Fatalf("a verified, present conversation must still produce --resume <own id>.\ncommand: %s", cmd)
	}
}

// TestClaudeSessionIDsMatch tables the matching rule, including the short
// prefix that carries too little entropy to be identity evidence.
func TestClaudeSessionIDsMatch(t *testing.T) {
	const full = "abcdef01-2345-4678-8abc-def012345678"
	cases := []struct {
		name           string
		recorded, cand string
		want           bool
	}{
		{"exact", full, full, true},
		{"case insensitive", full, strings.ToUpper(full), true},
		{"whitespace trimmed", full, "  " + full + "\n", true},
		{"candidate is prefix", full, full[:16], true},
		{"recorded is prefix", full[:20], full, true},
		{"prefix too short", full, full[:8], false},
		{"different ids", full, "99999999-9999-4999-8999-999999999999", false},
		{"empty recorded", "", full, false},
		{"empty candidate", full, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeSessionIDsMatch(tc.recorded, tc.cand); got != tc.want {
				t.Fatalf("claudeSessionIDsMatch(%q, %q) = %v, want %v", tc.recorded, tc.cand, got, tc.want)
			}
		})
	}
}

// TestResumeGuard_VerifiedSourcesClearDiscoveryTaint: once a source that
// identifies THIS session confirms the id (own tmux env, own hook payload,
// explicit --session-id in its own command), the id becomes recorded and
// resumable again.
func TestResumeGuard_VerifiedSourcesClearDiscoveryTaint(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const id = "dddddddd-4444-4555-8666-777777777777"
	inst.adoptDiscoveredClaudeSessionID(id)
	if inst.recordedClaudeSessionID() != "" {
		t.Fatal("precondition: a discovered id is not recorded ownership")
	}

	inst.markClaudeSessionIDVerified()
	if inst.recordedClaudeSessionID() != id {
		t.Fatalf("a verified id must be recorded; got %q", inst.recordedClaudeSessionID())
	}

	// Suspicion is keyed to the VALUE and does not expire: a writer cannot
	// launder a scanned id by moving the field away and back again. This is
	// the property that makes the guard unbypassable by re-assignment.
	inst.adoptDiscoveredClaudeSessionID(id)
	const boundElsewhere = "eeeeeeee-5555-4666-8777-888888888888"
	inst.ClaudeSessionID = boundElsewhere
	if inst.recordedClaudeSessionID() != boundElsewhere {
		t.Fatalf("an id that never came from a disk scan is ownable; got %q", inst.recordedClaudeSessionID())
	}
	inst.ClaudeSessionID = id // back to the scanned value
	if got := inst.recordedClaudeSessionID(); got != "" {
		t.Fatalf("re-assigning a previously scanned id must NOT launder it into ownership; got %q", got)
	}
	inst.markClaudeSessionIDVerified()
	if inst.recordedClaudeSessionID() != id {
		t.Fatalf("an explicit vouch must clear the suspicion; got %q", inst.recordedClaudeSessionID())
	}
}

// TestResumeGuard_TaintIsPersisted covers the bypass that the in-memory-only
// taint left open: a writer that SAVES a discovered conversation id without
// ever going through the resume builder (`switch-account --no-restart` is the
// plain case) would put an unverified id on disk, and the next process to load
// it would see a plain recorded id and resume it. The taint therefore rides
// the tool_data blob alongside the id.
func TestResumeGuard_TaintIsPersisted(t *testing.T) {
	const id = "f0f0f0f0-6666-4777-8888-999999999999"

	blob := WriteClaudeSessionUnverifiedToToolData(nil, true)
	if !ReadClaudeSessionUnverifiedFromToolData(blob) {
		t.Fatal("an unverified id must round-trip as unverified")
	}
	// Clearing writes an explicit false rather than deleting the key: the
	// extras merge preserves keys missing from the replacement blob, so a
	// delete would let a stale `true` merge back and strand a since-verified
	// session as permanently unresumable.
	cleared := WriteClaudeSessionUnverifiedToToolData(blob, false)
	if ReadClaudeSessionUnverifiedFromToolData(cleared) {
		t.Fatal("clearing the taint must read back as verified")
	}
	if !strings.Contains(string(cleared), toolDataClaudeSessionUnverifiedKey) {
		t.Fatalf("the cleared verdict must be written explicitly, not deleted: %s", cleared)
	}
	// A legacy row has no key at all and reads as verified — the pre-#1815
	// status quo for ids already on disk.
	if ReadClaudeSessionUnverifiedFromToolData([]byte(`{"claude_session_id":"x"}`)) {
		t.Fatal("a legacy row must not read as tainted")
	}

	// Write side: the flag is derived from the instance, so it can never
	// disagree with the id it describes.
	inst := &Instance{ID: "persist-taint", Tool: "claude"}
	inst.adoptDiscoveredClaudeSessionID(id)
	if !inst.claudeSessionIDIsUnverified() {
		t.Fatal("a discovered id must be persisted as unverified")
	}
	inst.markClaudeSessionIDVerified()
	if inst.claudeSessionIDIsUnverified() {
		t.Fatal("a verified id must be persisted as verified")
	}

	// Load side: a persisted taint is re-applied, so a process restart cannot
	// launder an unverified id into a resumable one.
	reloaded := &Instance{
		ID:                           "persist-taint",
		Tool:                         "claude",
		ClaudeSessionID:              id,
		claudeSessionIDsFromDiskScan: restoreClaudeSessionVerification(id, true),
	}
	if got := reloaded.recordedClaudeSessionID(); got != "" {
		t.Fatalf("a reloaded tainted id must not count as recorded; got %q", got)
	}
	if decision := reloaded.resumeIdentityAllowed(id); decision.Allow {
		t.Fatal("a reloaded tainted id must still be refused for --resume")
	}
	if restoreClaudeSessionVerification(id, false) != nil {
		t.Fatal("a pre-#1815 / untainted row must load clean so it keeps resuming")
	}
}

// TestResumeGuard_MigrateFallbackNeverLaunders pins the adversarial-review
// finding: MigrateConversationFrom's "newest conversation in the project dir"
// fallback picks a DIFFERENT conversation by mtime, and that is equally a
// guess whether or not a stale id was stored. Marking it only in the
// empty-id case let a stale-id session adopt (and resume) a neighbour's
// transcript.
func TestResumeGuard_MigrateFallbackNeverLaunders(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)
	inst.Tool = "claude"

	src, dst := filepath.Join(home, "src-cfg"), filepath.Join(home, "dst-cfg")
	projDir := filepath.Join(src, "projects", ConvertToClaudeDirName(inst.ProjectPath))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir src project dir: %v", err)
	}
	const neighbourID = "a1a1a1a1-7777-4888-8999-aaaaaaaaaaaa"
	body := `{"type":"user","sessionId":"` + neighbourID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, neighbourID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write neighbour transcript: %v", err)
	}

	// A STALE stored id: its file is gone, so the fallback fires.
	inst.ClaudeSessionID = "deadbeef-0000-4000-8000-000000000000"
	inst.markClaudeSessionIDVerified()

	if _, err := MigrateConversationFrom(inst, src, dst); err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	if inst.ClaudeSessionID != neighbourID {
		t.Fatalf("precondition: the fallback should have adopted the newest transcript; got %q", inst.ClaudeSessionID)
	}
	if got := inst.recordedClaudeSessionID(); got != "" {
		t.Fatalf("a mtime-chosen replacement is not owned just because an older id existed; recorded = %q", got)
	}
	if decision := inst.resumeIdentityAllowed(neighbourID); decision.Allow {
		t.Fatal("the migrated-by-guess conversation must not authorize --resume")
	}
}

// TestResumeGuard_TmuxCannotLaunderAScannedID pins the laundering loop the
// adversarial pass found: agent-deck writes CLAUDE_SESSION_ID into the pane
// and reads it back as an ownership signal, so publishing a disk-scanned id
// would let it return as "verified" on the next poll.
func TestResumeGuard_TmuxCannotLaunderAScannedID(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const scanned = "b2b2b2b2-8888-4999-8aaa-bbbbbbbbbbbb"
	inst.adoptDiscoveredClaudeSessionID(scanned)

	// A pane read must NOT absolve a scanned id...
	inst.noteClaudeSessionIDFromOwnPane()
	if got := inst.recordedClaudeSessionID(); got != "" {
		t.Fatalf("reading our own published value back must not verify it; got %q", got)
	}
	// ...while a value no scan produced is ownable through the same path.
	const minted = "c3c3c3c3-9999-4aaa-8bbb-cccccccccccc"
	inst.ClaudeSessionID = minted
	inst.noteClaudeSessionIDFromOwnPane()
	if inst.recordedClaudeSessionID() != minted {
		t.Fatalf("a never-scanned id from our own pane is ownable; got %q", inst.recordedClaudeSessionID())
	}
	// A source that is not downstream of us still clears the suspicion.
	inst.ClaudeSessionID = scanned
	inst.markClaudeSessionIDVerified()
	if inst.recordedClaudeSessionID() != scanned {
		t.Fatal("an explicit vouch must still clear a scan suspicion")
	}
}

// TestResumeGuard_ExplicitResumeInCommandIsAdopted covers the custom-command
// bypass: `claude --resume <id>` baked into the session's own command executes
// verbatim, so the id must be adopted (and owned) rather than left invisible
// to the chokepoint.
func TestResumeGuard_ExplicitResumeInCommandIsAdopted(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const explicit = "d4d4d4d4-aaaa-4bbb-8ccc-dddddddddddd"
	// Pre-existing suspicion on the very value the operator names must clear.
	inst.adoptDiscoveredClaudeSessionID(explicit)
	inst.ClaudeSessionID = ""
	inst.Command = "claude --resume " + explicit + " --model opus"

	if !inst.adoptExplicitClaudeSessionID("test") {
		t.Fatal("an embedded --resume <uuid> must be adopted as an explicit ownership declaration")
	}
	if inst.ClaudeSessionID != explicit {
		t.Fatalf("adopted id = %q, want %q", inst.ClaudeSessionID, explicit)
	}
	if inst.recordedClaudeSessionID() != explicit {
		t.Fatal("the vouch must apply to the EXPLICIT value, not to whatever id was current before it")
	}
}

// TestResumeGuard_ContinueModePrefersOwnConversation pins the `-c` hole:
// `claude -c` picks the newest conversation in the directory inside the CLI,
// where this guard cannot see it. With an owned id, resume that instead.
func TestResumeGuard_ContinueModePrefersOwnConversation(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := newGuardInstance(t, home)

	const ownID = "e5e5e5e5-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	inst.ClaudeSessionID = ownID
	inst.markClaudeSessionIDVerified()

	opts := NewClaudeOptions(nil)
	opts.SessionMode = "continue"
	if err := inst.SetClaudeOptions(opts); err != nil {
		t.Fatalf("SetClaudeOptions: %v", err)
	}

	cmd := inst.buildClaudeCommand("claude")
	if strings.Contains(cmd, " -c") {
		t.Fatalf("#1815: with an owned conversation id, continue mode must not defer to the CLI's newest-in-dir pick.\ncommand: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume "+ownID) {
		t.Fatalf("#1815: continue mode must resume this session's own conversation.\ncommand: %s", cmd)
	}
}

// TestResumeGuard_TaintSurvivesClearedIDSave pins a review finding on #1830:
// the persisted taint was silently stripped while the tainted id itself
// survived, defeating the PR's own persistence guarantee.
//
// Failure shape: an instance holds a disk-scanned (tainted) id A on disk.
// Something clears the in-memory ClaudeSessionID to "" and saves (e.g.
// RestartFresh's clearSessionBindingForFreshStart, or the stale-snapshot
// race the sticky merge rule exists for). claude_session_id is a STICKY
// extras key, so the OLD row's id A is carried forward across that save
// even though the new blob omits it. But if the taint marker were written
// explicitly as `false` whenever the in-memory id is empty (as it read
// unverified() == false for id==""), that explicit false would win over the
// old row's `true` in the extras merge -- id A survives, taint erased. A's
// resume would then be silently authorized on the next load.
//
// The fix: instanceToRow must OMIT the taint marker (not write explicit
// false) when inst.ClaudeSessionID is empty, so it rides along with the
// same sticky-preserved old id instead of being overwritten.
func TestResumeGuard_TaintSurvivesClearedIDSave(t *testing.T) {
	const id = "b2b2b2b2-3333-4444-8555-666666666666"

	// Step 1: instance has a tainted id and is saved -- old row on disk has
	// claude_session_id=A and claude_session_id_unverified=true.
	tainted := &Instance{ID: "taint-survive", Tool: "claude"}
	tainted.adoptDiscoveredClaudeSessionID(id)
	oldRow, err := instanceToRow(tainted)
	if err != nil {
		t.Fatalf("instanceToRow (tainted): %v", err)
	}
	if !ReadClaudeSessionUnverifiedFromToolData(oldRow.ToolData) {
		t.Fatal("setup: the first save must persist the taint")
	}

	// Step 2: the in-memory id is cleared (e.g. a fresh-start reset) and the
	// instance is saved again -- the new blob must OMIT the marker.
	cleared := &Instance{ID: "taint-survive", Tool: "claude"}
	cleared.ClaudeSessionID = ""
	newRow, err := instanceToRow(cleared)
	if err != nil {
		t.Fatalf("instanceToRow (cleared): %v", err)
	}
	if strings.Contains(string(newRow.ToolData), toolDataClaudeSessionUnverifiedKey) {
		t.Fatalf("a save with an empty ClaudeSessionID must OMIT the taint marker, not write it explicitly: %s", newRow.ToolData)
	}

	// Step 3: simulate the statedb extras merge that runs on every SaveInstance.
	merged := statedb.MergeToolDataExtras(oldRow.ToolData, newRow.ToolData)
	if !ReadClaudeSessionUnverifiedFromToolData(merged) {
		t.Fatalf("the taint must survive a save whose in-memory id was cleared, or the sticky-preserved id %q would read back as verified: merged=%s", id, merged)
	}
}

// TestResumeGuard_LiveHookConfirmationClearsTaint pins a review finding on
// #1830: when a live hook payload reports an id that EQUALS this instance's
// already-recorded (but disk-scan-tainted) id, that equality branch was
// treated as a no-op and skipped the vouch. That is backwards -- a live
// process's own hook confirming its own id is the STRONGEST available
// evidence, and failing to clear the taint on it meant a correctly-recovered
// solo session (its own transcript was the only one in its cwd) stayed
// permanently unresumable.
func TestResumeGuard_LiveHookConfirmationClearsTaint(t *testing.T) {
	const id = "c3c3c3c3-4444-4555-8666-777777777777"

	inst := &Instance{ID: "hook-confirms-taint", Tool: "claude"}
	inst.adoptDiscoveredClaudeSessionID(id)
	if !inst.claudeSessionIDIsUnverified() {
		t.Fatal("setup: id must start tainted")
	}

	// A hook payload whose session id equals the already-recorded (tainted)
	// id must clear the taint, not leave it in place.
	inst.hookSessionID = id
	if inst.hookSessionID != inst.ClaudeSessionID {
		t.Fatal("setup: hookSessionID must equal ClaudeSessionID to hit the equality branch")
	}
	inst.markClaudeSessionIDVerified() // what the equality branch must call

	if inst.claudeSessionIDIsUnverified() {
		t.Fatal("a live hook payload confirming this instance's own recorded id must clear the disk-scan taint")
	}
	if decision := inst.resumeIdentityAllowed(id); !decision.Allow {
		t.Fatalf("after hook confirmation the id must be resumable, got refused: %s", decision.Reason)
	}
}

// TestResumeGuard_ForkResumeIDNeverAdoptedAsOwnIdentity pins a review
// finding on #1830: adoptExplicitClaudeSessionID's `--resume` ownership
// fallback (added so a custom command carrying `claude --resume <uuid>`
// still meets the chokepoint) could adopt a FOREIGN conversation id and mark
// it verified when the command matched the fork builder's shape --
// `claude --session-id "$SESSION_ID" --resume <parent-uuid> --fork-session`
// -- because the `--session-id` extraction bails on the first
// non-bare-UUID argument (here, an unexpanded shell variable) without
// scanning further, and the code fell through to treating the fork's
// `--resume <parent-uuid>` as this session's own declared identity. That is
// precisely the child-adopts-parent's-conversation shape #1815 exists to
// stop.
func TestResumeGuard_ForkResumeIDNeverAdoptedAsOwnIdentity(t *testing.T) {
	const parentID = "d4d4d4d4-5555-4666-8777-888888888888"

	inst := &Instance{
		ID:      "fork-resume-guard",
		Tool:    "claude",
		Command: `claude --session-id "$SESSION_ID" --resume ` + parentID + ` --fork-session`,
	}
	if inst.adoptExplicitClaudeSessionID("test") {
		t.Fatalf("a command shaped like the fork builder must not be treated as an explicit ownership declaration; adopted id=%q", inst.ClaudeSessionID)
	}
	if inst.ClaudeSessionID == parentID {
		t.Fatal("the fork source's (parent's) conversation id must never be adopted as this instance's own identity")
	}

	// Sanity: a genuine standalone `claude --resume <uuid>` (no fork
	// markers) must still be honored -- this guard must not regress that.
	standalone := &Instance{
		ID:      "standalone-resume",
		Tool:    "claude",
		Command: `claude --resume ` + parentID,
	}
	if !standalone.adoptExplicitClaudeSessionID("test") {
		t.Fatal("a standalone --resume <uuid> command (no --session-id/--fork-session) must still be adopted")
	}
	if standalone.ClaudeSessionID != parentID {
		t.Fatalf("standalone ClaudeSessionID = %q, want %q", standalone.ClaudeSessionID, parentID)
	}
}
