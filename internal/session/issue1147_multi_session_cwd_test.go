package session

// Issue #1147 — discoverLatestClaudeJSONL hijacks --session-id in
// multi-session-per-cwd setups.
//
// Root-cause analysis by @KrE80r (filed 2026-05-22):
//
// A user runs 5 agent-deck sessions in the same project directory (one per
// tenant scope). Each session's launch -cmd bakes in a distinct
// `claude --session-id <fresh-uuid> ...` so each tenant owns its own Claude
// conversation. Because the wrapper script overrides CLAUDE_CONFIG_DIR,
// agent-deck's happy-path session-id capture (hooks) never fires, leaving
// i.ClaudeSessionID empty on Instance even after live conversations exist
// on disk under ~/.claude/projects/<encoded-cwd>/.
//
// On Restart(), ensureClaudeSessionIDFromDiskForRestart() (instance.go:2613)
// walks the shared cwd, picks the newest <uuid>.jsonl by mtime, and
// overwrites i.ClaudeSessionID with that UUID — **even though the user's
// Command explicitly names a different one**. All 5 sessions converge on
// whichever sibling's transcript is freshest; the duplicate-session sweeper
// (tmux.KillSessionsWithEnvValue at instance.go:2786) then kills 4 of 5 for
// sharing the same CLAUDE_SESSION_ID. The loop repeats every ~90 seconds.
//
// The bypass at instance.go:2613 is PR #956 (commit d1590d66), which
// correctly restored history-resume for single-session-per-cwd custom
// wrappers. The gate it bypasses is PR #608 (commit 4b7a033a). When N
// sessions share one cwd, the #956 bypass silently regresses #608.
//
// Fix: respect an explicit `--session-id <uuid>` baked into i.Command. If
// present, that UUID is the source of truth and supersedes BOTH any
// previously-bound auto-discovered ID AND any disk-discovered candidate.
// If absent, fall through to the existing #956 disk-discovery (single-
// session-per-cwd recovery preserved).
//
// These four tests pin the post-fix contract:
//
//   1. Multi-session-per-cwd with explicit IDs: each session keeps its own.
//   2. Single-session-per-cwd with NO explicit ID: disk discovery still
//      binds the JSONL UUID (#956 unbroken).
//   3. Five-session pile-up: after the restart prelude, all five UUIDs are
//      distinct — the precondition that prevents the dup-sweeper firing.
//   4. Single-session-per-cwd RESTART with custom wrapper: the #956 fix
//      scenario verbatim, must still auto-bind.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stageJSONL writes a UUID-named JSONL transcript under the isolated HOME's
// Claude projects dir for projectPath, backdated to mtime so callers can
// stage deterministic mtime ordering across multiple files.
func stageJSONL(t *testing.T, home, projectPath, uuid string, mtime time.Time) {
	t.Helper()
	projectDir := claudeProjectDirForTest(t, filepath.Join(home, ".claude"), projectPath)
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	path := filepath.Join(projectDir, uuid+".jsonl")
	body := []byte(`{"sessionId":"` + uuid + `","role":"user","content":"hi"}` + "\n")
	require.NoError(t, os.WriteFile(path, body, 0o644))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	t.Cleanup(func() { _ = os.Remove(path) })
}

// newClaudeInstanceForExplicitID is a minimal-fixture variant of
// newClaudeInstanceForDispatch that does NOT start tmux. The restart-path
// disk-discovery prelude is a pure function over (Command, ProjectPath,
// $HOME/.claude/projects/*) and needs no live tmux.
func newClaudeInstanceForExplicitID(t *testing.T, home, command string) *Instance {
	t.Helper()
	projectPath := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(projectPath, 0o755))
	return &Instance{
		ID:               "test-1147",
		Tool:             "claude",
		ProjectPath:      projectPath,
		Command:          command,
		ClaudeSessionID:  "",
		ClaudeDetectedAt: time.Now().Add(-1 * time.Hour), // not zero — past the #608 gate
	}
}

// TestIssue1147_Test1_ExplicitID_TwoSessions_SameCWD pins the core multi-
// tenant invariant: two sessions in the same cwd, each launched with its
// own explicit --session-id, MUST each retain its own UUID after the
// restart-path disk-discovery prelude runs.
//
// RED on main: discoverLatestClaudeJSONL picks the newest JSONL by mtime.
// Both sessions get the SAME UUID (whichever wrote most recently). The
// dup-sweeper would then fire on a real Start.
//
// GREEN with fix: the explicit-ID extractor sees `--session-id <uuid>` in
// each session's Command, adopts that UUID, and skips disk discovery
// entirely.
func TestIssue1147_Test1_ExplicitID_TwoSessions_SameCWD(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	const (
		uuidA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		uuidB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	cmdA := "env APP_TENANT_SCOPE=a claude --session-id " + uuidA + " --dangerously-skip-permissions"
	cmdB := "env APP_TENANT_SCOPE=b claude --session-id " + uuidB + " --dangerously-skip-permissions"

	instA := newClaudeInstanceForExplicitID(t, home, cmdA)
	instB := newClaudeInstanceForExplicitID(t, home, cmdB)
	// Both sessions share a cwd — mirror the real bug scenario.
	instB.ProjectPath = instA.ProjectPath

	// Stage two JSONLs in the shared cwd, with B newer than A by mtime.
	now := time.Now()
	stageJSONL(t, home, instA.ProjectPath, uuidA, now.Add(-30*time.Second))
	stageJSONL(t, home, instA.ProjectPath, uuidB, now)

	// Also stage a hostile third "sibling" UUID — newest on mtime — to
	// model the issue's exact evidence: a third session's JSONL would
	// hijack both A and B if disk discovery wins.
	const uuidHostile = "ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb"
	stageJSONL(t, home, instA.ProjectPath, uuidHostile, now.Add(1*time.Second))

	instA.ensureClaudeSessionIDFromDiskForRestart()
	instB.ensureClaudeSessionIDFromDiskForRestart()

	require.Equal(t, uuidA, instA.ClaudeSessionID,
		"Issue #1147: instance with `--session-id %s` in Command must adopt that UUID, NOT the newest sibling JSONL (%s). Got %q.",
		uuidA, uuidHostile, instA.ClaudeSessionID)
	require.Equal(t, uuidB, instB.ClaudeSessionID,
		"Issue #1147: instance with `--session-id %s` in Command must adopt that UUID, NOT the newest sibling JSONL (%s). Got %q.",
		uuidB, uuidHostile, instB.ClaudeSessionID)
	require.NotEqual(t, instA.ClaudeSessionID, instB.ClaudeSessionID,
		"Issue #1147: two sessions in the same cwd with distinct explicit --session-id values must NOT converge on the same UUID — that is the precondition the duplicate-session sweeper relies on.")
}

// TestIssue1147_Test2_NoExplicitID_DiskDiscoveryStillWorks pins #956 parity:
// a session WITHOUT --session-id in its Command (custom wrapper that lets
// claude mint its own UUID) MUST still auto-bind from disk on restart so
// chat history survives.
func TestIssue1147_Test2_NoExplicitID_DiskDiscoveryStillWorks(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	// Custom wrapper, no --session-id baked in. This is the #956 scenario.
	inst := newClaudeInstanceForExplicitID(t, home, "/usr/local/bin/my-wrapper.sh")

	const jsonlUUID = "95600000-9560-9560-9560-956000000956"
	stageJSONL(t, home, inst.ProjectPath, jsonlUUID, time.Now())

	inst.ensureClaudeSessionIDFromDiskForRestart()

	require.Equal(t, jsonlUUID, inst.ClaudeSessionID,
		"Issue #1147 must NOT regress #956: a custom-wrapper session with no explicit --session-id must still auto-bind to the newest JSONL on restart. Got %q.",
		inst.ClaudeSessionID)
}

// TestIssue1147_Test3_FiveSessionsKeepDistinctIDs_DupSweepDoesNotFire models
// the user's exact scenario (5 tenant sessions sharing one cwd). After the
// restart prelude runs on all five, each must hold its own explicit UUID.
// Distinct UUIDs are the structural precondition that prevents the
// duplicate-session sweeper (instance.go:2786, tmux.KillSessionsWithEnvValue)
// from firing.
//
// We don't invoke the sweeper directly here because it operates on real
// tmux sessions; the equivalent assertion is "no two instances share an ID",
// which is what the sweeper checks. The sweeper test path is covered by
// issue666_restart_sweep_test.go for the case where IDs legitimately collide.
func TestIssue1147_Test3_FiveSessionsKeepDistinctIDs_DupSweepDoesNotFire(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	uuids := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444",
		"55555555-5555-5555-5555-555555555555",
	}
	names := []string{"TenantA", "TenantB", "TenantC", "TenantD", "Conductor"}

	// Hostile sibling JSONL — newest on mtime, owned by no tenant. The
	// pre-fix dispatcher would assign this UUID to ALL five.
	const uuidHostile = "deadbeef-dead-beef-dead-beefdeadbeef"

	// One project dir shared by all five.
	sharedProject := filepath.Join(home, "myapp")
	require.NoError(t, os.MkdirAll(sharedProject, 0o755))

	insts := make([]*Instance, len(uuids))
	now := time.Now()
	for idx, u := range uuids {
		cmd := "env APP_TENANT_SCOPE=" + names[idx] + " claude --session-id " + u + " --dangerously-skip-permissions"
		insts[idx] = newClaudeInstanceForExplicitID(t, home, cmd)
		insts[idx].ProjectPath = sharedProject
		insts[idx].ID = "test-1147-" + names[idx]
		// Each tenant's JSONL exists, but the hostile one will be newer.
		stageJSONL(t, home, sharedProject, u, now.Add(-time.Duration(idx+10)*time.Second))
	}
	stageJSONL(t, home, sharedProject, uuidHostile, now)

	for _, inst := range insts {
		inst.ensureClaudeSessionIDFromDiskForRestart()
	}

	seen := make(map[string]string)
	for idx, inst := range insts {
		require.Equal(t, uuids[idx], inst.ClaudeSessionID,
			"Issue #1147: %s must adopt its explicit --session-id %s, not the newest sibling JSONL %s. Got %q.",
			names[idx], uuids[idx], uuidHostile, inst.ClaudeSessionID)
		if prior, ok := seen[inst.ClaudeSessionID]; ok {
			t.Fatalf("Issue #1147: %s and %s both ended up with ClaudeSessionID=%q. With ANY collision the duplicate-session sweeper kills one of them on next Start().",
				prior, names[idx], inst.ClaudeSessionID)
		}
		seen[inst.ClaudeSessionID] = names[idx]
	}
	require.Len(t, seen, len(uuids), "all five session IDs must be distinct")
}

// TestIssue1147_Test4_SingleSessionPerCWD_RestartStillAutoBinds is the
// belt-and-suspenders test for #956: when there is exactly ONE custom-
// wrapper session in a cwd (no explicit --session-id anywhere), the
// restart-path disk discovery MUST still bind the JSONL. This is what the
// #956 bypass exists to enable; the #1147 fix must narrow the bypass
// without removing it.
func TestIssue1147_Test4_SingleSessionPerCWD_RestartStillAutoBinds(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	// Single session, custom wrapper, no explicit --session-id. The classic
	// #956 case: conductor restart must resume history.
	inst := newClaudeInstanceForExplicitID(t, home, "/opt/launch-claude.sh")
	// #956 also requires ClaudeDetectedAt to be zero on the bug path
	// (Start()'s #608 gate would early-return, so the restart-path fix
	// must do the work). Mirror that precondition exactly.
	inst.ClaudeDetectedAt = time.Time{}

	const jsonlUUID = "95611470-9561-1470-9561-147009561147"
	stageJSONL(t, home, inst.ProjectPath, jsonlUUID, time.Now())

	inst.ensureClaudeSessionIDFromDiskForRestart()

	require.Equal(t, jsonlUUID, inst.ClaudeSessionID,
		"Issue #1147 fix must NOT regress #956: a single custom-wrapper session per cwd, with no explicit --session-id and ClaudeDetectedAt zero, MUST still auto-bind to the newest JSONL on the restart path. Got %q.",
		inst.ClaudeSessionID)
	require.False(t, inst.ClaudeDetectedAt.IsZero(),
		"Issue #1147 fix must preserve #956's write-through of ClaudeDetectedAt so subsequent operations see a populated capture time.")
}
