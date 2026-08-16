package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/1851.
//
// An --ssh session's conversation lives on the remote host, but everything that
// resolves a transcript on the controller is keyed on Instance.ProjectPath —
// which for a remote session is only a LOCAL placeholder, defaulting to the
// directory `add --ssh` was run in. So a lookup does not miss; it HITS, on
// whatever local session sits at that directory. The reported consequence is
// data loss: the remote adopts the local session's conversation UUID, and
// restart()'s sweepDuplicateToolSessions then kills the local tmux session that
// legitimately owns it.
//
// The fixture below is deliberately the WORST case — a local session and a
// remote session at the same placeholder path, with a real transcript on local
// disk. Each test asserts the remote refuses AND that the identical local
// lookup still succeeds, so a guard that simply broke the feature would fail.

const localReplyText = "LOCAL SESSION SECRET REPLY"

// remoteBoundaryFixture seeds a local Claude transcript under projectPath and
// returns a local instance that owns it plus a remote instance that merely
// stores the same path as its placeholder.
func remoteBoundaryFixture(t *testing.T) (local, remote *Instance, sessionID, projectPath string) {
	t.Helper()

	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath = t.TempDir()
	sessionID = "11111111-2222-3333-4444-555555555555"

	projDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	// The "sessionId" field is what sessionHasConversationData looks for to
	// decide the conversation was actually interacted with.
	line := `{"sessionId":"` + sessionID + `","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + localReplyText + `"}]}}`
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	local = NewInstanceWithTool("local-owner", projectPath, "claude")
	local.ClaudeSessionID = sessionID
	local.ClaudeDetectedAt = time.Now()

	remote = NewInstanceWithTool("remote-session", projectPath, "claude")
	remote.SSHHost = "alice@host-a"
	remote.SSHRemotePath = "/srv/app-a"

	return local, remote, sessionID, projectPath
}

// --- Door 1 and 2: session-id adoption from disk ------------------------------

// TestEnsureClaudeSessionIDFromDiskForRestart_RemoteNeverAdoptsLocalUUID is the
// reported bug. Every remote restart reaches this function (a remote session's
// local ClaudeSessionID is essentially always empty), and the mtime walk it does
// scans the CONTROLLER's projects dir.
func TestEnsureClaudeSessionIDFromDiskForRestart_RemoteNeverAdoptsLocalUUID(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)

	remote.ensureClaudeSessionIDFromDiskForRestart()
	if remote.ClaudeSessionID != "" {
		t.Fatalf("remote session adopted a LOCAL conversation UUID %q on restart; the dup sweeper would then kill the local session that owns it", remote.ClaudeSessionID)
	}

	// The same walk must still work for a local session, or the guard has just
	// broken restart-resume rather than scoped it.
	local.ClaudeSessionID = ""
	local.ensureClaudeSessionIDFromDiskForRestart()
	if local.ClaudeSessionID != sessionID {
		t.Fatalf("local restart discovery regressed: got %q, want %q", local.ClaudeSessionID, sessionID)
	}
}

// TestEnsureClaudeSessionIDFromDisk_RemoteNeverAdoptsLocalUUID covers the Start
// path. It is only ACCIDENTALLY safe today via the #608 ClaudeDetectedAt gate,
// and that gate is re-openable through `session set <id> claude-session-id X`.
func TestEnsureClaudeSessionIDFromDisk_RemoteNeverAdoptsLocalUUID(t *testing.T) {
	_, remote, _, _ := remoteBoundaryFixture(t)

	// Stamp ClaudeDetectedAt then clear the id: exactly the state
	// `session set claude-session-id` leaves behind, which opens the #608 gate.
	remote.ClaudeDetectedAt = time.Now()
	remote.ClaudeSessionID = ""

	remote.ensureClaudeSessionIDFromDisk()
	if remote.ClaudeSessionID != "" {
		t.Fatalf("remote session adopted LOCAL conversation UUID %q on the Start path", remote.ClaudeSessionID)
	}
}

// TestSweepDuplicateToolSessions_RemoteHasNoLocalIDToSweepWith is required
// behaviour 5: the sweeper must never kill a session because a remote session
// picked up its conversation id. With adoption blocked the remote reaches the
// sweep holding no tool session id at all, so the CLAUDE_SESSION_ID sweep cannot
// name anyone else's conversation.
func TestSweepDuplicateToolSessions_RemoteHasNoLocalIDToSweepWith(t *testing.T) {
	_, remote, sessionID, _ := remoteBoundaryFixture(t)

	var sweptVars, sweptValues []string
	orig := killDuplicateSessionsFn
	killDuplicateSessionsFn = func(envKey, envValue, excludeName string) {
		sweptVars = append(sweptVars, envKey)
		sweptValues = append(sweptValues, envValue)
	}
	t.Cleanup(func() { killDuplicateSessionsFn = orig })

	remote.ensureClaudeSessionIDFromDiskForRestart()
	remote.tmuxSession = remote.GetTmuxSession()
	remote.sweepDuplicateToolSessions()

	for i, v := range sweptValues {
		if sweptVars[i] == "CLAUDE_SESSION_ID" && v == sessionID {
			t.Fatalf("the sweeper was handed the LOCAL session's conversation id %q — it would kill the local tmux session that owns it", v)
		}
	}
}

// --- Door 3: the disk scan that WRITES its result back ------------------------

// TestFindLatestClaudeTranscriptOnDisk_RemoteRefuses covers review finding F2:
// the third door, and the most dangerous one, because GetLastResponseBestEffort
// stores what it finds onto the instance and into the pane's tmux env — which
// then reaches the sweeper past the restart bail-out.
func TestFindLatestClaudeTranscriptOnDisk_RemoteRefuses(t *testing.T) {
	local, remote, _, _ := remoteBoundaryFixture(t)

	if id, resp := remote.findLatestClaudeTranscriptOnDisk(); id != "" || resp != nil {
		t.Fatalf("remote session found a LOCAL transcript on disk: id=%q resp=%+v", id, resp)
	}
	if id, resp := local.findLatestClaudeTranscriptOnDisk(); id == "" || resp == nil {
		t.Fatalf("local disk scan regressed: id=%q resp=%+v", id, resp)
	}
}

// TestGetLastResponseBestEffort_RemoteNeverReturnsLocalConversation is the same
// door seen from its caller: the observable harm is that `session output` on a
// remote session returns a local session's reply.
func TestGetLastResponseBestEffort_RemoteNeverReturnsLocalConversation(t *testing.T) {
	_, remote, _, _ := remoteBoundaryFixture(t)

	resp, err := remote.GetLastResponseBestEffort()
	if err == nil && resp != nil && strings.Contains(resp.Content, localReplyText) {
		t.Fatalf("remote session returned the LOCAL session's conversation: %q", resp.Content)
	}
	if remote.ClaudeSessionID != "" {
		t.Fatalf("remote session durably adopted local conversation id %q via the read path", remote.ClaudeSessionID)
	}
}

// --- Doors 4 and 5: path resolution and the structured read -------------------

// TestGetJSONLPath_RemoteResolvesToNothing pins the rule that makes the
// claudeTranscriptDir key change safe (review finding F1): no remote instance
// resolves a local transcript path at all.
func TestGetJSONLPath_RemoteResolvesToNothing(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)
	remote.ClaudeSessionID = sessionID // as if set by `session set claude-session-id`

	if got := remote.GetJSONLPath(); got != "" {
		t.Fatalf("remote session resolved a LOCAL transcript path %q", got)
	}
	if got := local.GetJSONLPath(); got == "" {
		t.Fatal("local transcript resolution regressed")
	}
}

// TestGetClaudeLastResponse_RemoteRefuses covers `session output`'s structured
// read.
func TestGetClaudeLastResponse_RemoteRefuses(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)
	remote.ClaudeSessionID = sessionID

	if resp, err := remote.getClaudeLastResponse(); err == nil {
		t.Fatalf("remote session read a LOCAL transcript: %+v", resp)
	}
	if _, err := local.getClaudeLastResponse(); err != nil {
		t.Fatalf("local structured read regressed: %v", err)
	}
}

// --- Review finding F1: the collision guard must NOT be disabled ---------------

// TestClaudeSessionIDCollision_LocalAndRemoteCannotShareOneTranscriptFile is the
// regression the previous attempt shipped and pinned as correct.
//
// claudeTranscriptDir keys remote sessions on where they RUN, so a local and a
// remote session sharing the placeholder path and a claude_session_id no longer
// "collide". That is only safe because the remote resolves to NO file at all —
// which is what this test checks, rather than checking the collision verdict in
// isolation. The property that matters is the one #1349/#1352 protect: two live
// instances must never be handed the same transcript path.
func TestClaudeSessionIDCollision_LocalAndRemoteCannotShareOneTranscriptFile(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)
	remote.ClaudeSessionID = sessionID
	local.Status = StatusRunning
	remote.Status = StatusRunning

	peers := []*Instance{local, remote}

	localPath, localErr := local.GetJSONLPathChecked(peers)
	remotePath, remoteErr := remote.GetJSONLPathChecked(peers)

	if remotePath != "" {
		t.Fatalf("remote session was handed a local transcript path %q (err=%v)", remotePath, remoteErr)
	}
	if localPath != "" && localPath == remotePath {
		t.Fatalf("a local and a remote session resolved to the SAME transcript file %q — the #1349 guard is off", localPath)
	}
	if localErr != nil {
		t.Fatalf("the local session lost access to its own transcript: %v", localErr)
	}
}

// TestClaudeSessionIDCollision_TwoLocalSessionsStillRefused is the other half of
// F1: narrowing the key must not weaken the guard for the pair it was written
// for. Two LOCAL live instances sharing a project dir and a session id still
// resolve to one file, and must still be refused.
func TestClaudeSessionIDCollision_TwoLocalSessionsStillRefused(t *testing.T) {
	local, _, sessionID, projectPath := remoteBoundaryFixture(t)
	local.Status = StatusRunning

	sibling := NewInstanceWithTool("local-sibling", projectPath, "claude")
	sibling.ClaudeSessionID = sessionID
	sibling.ClaudeDetectedAt = time.Now()
	sibling.Status = StatusRunning

	peers := []*Instance{local, sibling}
	if !local.ClaudeSessionIDCollidesWith(peers) {
		t.Fatal("two LOCAL live instances sharing a project dir and a claude_session_id are no longer detected as colliding — the #1349/#1352 guard was weakened")
	}
	if _, err := local.GetJSONLPathChecked(peers); err == nil {
		t.Fatal("GetJSONLPathChecked stopped refusing a genuine local collision")
	}
}

// TestClaudeTranscriptDir_RemoteSessionsAreKeyedByWhereTheyRun is the #1851
// half of the key change: two remote sessions that merely share a local
// placeholder are not in the same place and must not be judged to share a
// transcript directory.
func TestClaudeTranscriptDir_RemoteSessionsAreKeyedByWhereTheyRun(t *testing.T) {
	_, remoteA, _, projectPath := remoteBoundaryFixture(t)

	remoteB := NewInstanceWithTool("remote-b", projectPath, "claude") // same placeholder
	remoteB.SSHHost = "bob@host-b"
	remoteB.SSHRemotePath = "/opt/app-b"

	if remoteA.claudeTranscriptDir() == remoteB.claudeTranscriptDir() {
		t.Fatal("two remote sessions on different hosts share a transcript key because their local placeholders match")
	}

	// Same host, same remote dir, different placeholders: genuinely co-located.
	remoteC := NewInstanceWithTool("remote-c", t.TempDir(), "claude")
	remoteC.SSHHost = remoteA.SSHHost
	remoteC.SSHRemotePath = remoteA.SSHRemotePath
	if remoteA.claudeTranscriptDir() != remoteC.claudeTranscriptDir() {
		t.Fatal("two sessions at the same host and remote path do not share a transcript key")
	}

	// The two spellings of the remote home are one place here too.
	remoteHomeA := NewInstanceWithTool("h1", t.TempDir(), "claude")
	remoteHomeA.SSHHost = "alice@host-a"
	remoteHomeA.SSHRemotePath = ""
	remoteHomeB := NewInstanceWithTool("h2", t.TempDir(), "claude")
	remoteHomeB.SSHHost = "alice@host-a"
	remoteHomeB.SSHRemotePath = "~"
	if remoteHomeA.claudeTranscriptDir() != remoteHomeB.claudeTranscriptDir() {
		t.Fatal(`remote paths "" and "~" produced different transcript keys, but they run in the same directory`)
	}

	// The key must never alias a real local projects/ directory.
	if strings.Contains(remoteA.claudeTranscriptDir(), filepath.Join("projects", ConvertToClaudeDirName(projectPath))) {
		t.Fatalf("remote transcript key aliases a local project dir: %s", remoteA.claudeTranscriptDir())
	}
}

// --- Doors 6 to 9: the conversation-data probes -------------------------------

func TestConversationDataProbes_RemoteRefuses(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)

	if sessionHasConversationData(remote, sessionID) {
		t.Error("sessionHasConversationData said a LOCAL transcript belongs to the remote session")
	}
	if !sessionHasConversationData(local, sessionID) {
		t.Error("sessionHasConversationData regressed for a local session")
	}

	if got := sessionConversationByteSize(remote, sessionID); got != 0 {
		t.Errorf("sessionConversationByteSize measured a local transcript for a remote session: %d", got)
	}
	if got := sessionConversationByteSize(local, sessionID); got == 0 {
		t.Error("sessionConversationByteSize regressed for a local session")
	}

	if got := sessionConversationMtime(remote, sessionID); !got.IsZero() {
		t.Errorf("sessionConversationMtime read a local transcript for a remote session: %v", got)
	}

	if got := findSessionFileInAllProjects(remote, sessionID); got != "" {
		t.Errorf("findSessionFileInAllProjects found %q for a remote session — this door does not even need the placeholder to collide", got)
	}
	if got := findSessionFileInAllProjects(local, sessionID); got == "" {
		t.Error("findSessionFileInAllProjects regressed for a local session")
	}
}

// --- Doors 10 to 14: handoff and migration ------------------------------------

func TestHandoffAndMigration_RemoteRefuses(t *testing.T) {
	local, remote, sessionID, _ := remoteBoundaryFixture(t)
	remote.ClaudeSessionID = sessionID

	if got := ClaudeTranscriptPathForInstance(remote); got != "" {
		t.Errorf("ClaudeTranscriptPathForInstance CONSTRUCTED a local path for a remote session: %q", got)
	}
	if got := ClaudeTranscriptPathForInstance(local); got == "" {
		t.Error("ClaudeTranscriptPathForInstance regressed for a local session")
	}

	if _, _, err := BuildClaudeToCodexHandoffPrompt(remote, 1000); err == nil {
		t.Error("session handoff read a LOCAL session's whole transcript for a remote session")
	}

	cfg, cfgErr := LoadUserConfig()
	if cfgErr == nil {
		if dir, _, _ := LocateConversationConfigDir(cfg, remote, GetClaudeConfigDir()); dir != "" {
			t.Errorf("LocateConversationConfigDir named %q for a remote session; switch-account then MIGRATES that file", dir)
		}
		if dir, _, _ := LocateConversationConfigDir(cfg, local, GetClaudeConfigDir()); dir == "" {
			t.Error("LocateConversationConfigDir regressed for a local session")
		}
	}

	if moved, err := MigrateConversationFrom(remote, GetClaudeConfigDir(), t.TempDir()); err != nil || moved != "" {
		t.Errorf("MigrateConversationFrom moved %q for a remote session (err=%v)", moved, err)
	}

	// The live transcript exists, so RestoreOrphanedConversationBackup is a
	// no-op for both; the point is that the remote never reaches the scan.
	if restored, err := RestoreOrphanedConversationBackup(remote, GetClaudeConfigDir()); err != nil || restored != "" {
		t.Errorf("RestoreOrphanedConversationBackup restored %q for a remote session (err=%v)", restored, err)
	}
}
