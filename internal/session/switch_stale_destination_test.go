// Regression coverage for three defects found in account switching on
// 2026-09-17 (session 87512772 "Germany Immigration", Claude session id
// 1a022ef6-...): (1) a "successful" switch could leave the session record's
// account field empty because storage persistence was a separate, skippable
// caller-side step; (2) switching back was a dead end when the destination
// still held the pre-switch copy, even though it was strictly older; (3) a
// failed journal blocked every subsequent retry forever.
package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setupSwitchStaleDestinationFixture wires a temp HOME with two configured
// Claude accounts, injects a no-tmux native lifecycle, and returns the config
// plus a helper to write a transcript for one of the accounts.
func setupSwitchStaleDestinationFixture(t *testing.T) (cfg *UserConfig, home, project string, restoreLifecycle func()) {
	t.Helper()
	home = withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.seminno.claude]
config_dir = "~/.claude-seminno"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	var err error
	cfg, err = LoadUserConfig()
	require.NoError(t, err)
	project = filepath.Join(home, "project")

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	nativeSwitchRunning = func(*Instance) bool { return false }
	nativeSwitchStop = func(*Instance) error { return nil }
	nativeSwitchStart = func(*Instance) error { return nil }
	restoreLifecycle = func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	}
	return cfg, home, project, restoreLifecycle
}

func writeAccountTranscript(t *testing.T, home, account, project, sid, body string) string {
	t.Helper()
	path := filepath.Join(home, ".claude-"+account, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// Defect 1: a switch that reports success must never leave storage without
// the new account. Previously the account mutation was written to SQLite only
// by a second, distinct caller step (CommitAccountSwitch); a caller that
// never took that step (or crashed between the two) left storage on the old,
// empty-looking account while the transcript already lived under the new
// one. Passing Storage makes ExecuteHarnessSwitch persist it itself, so the
// registry is already correct before this test ever calls CommitAccountSwitch.
func TestSwitchStaleDestination_AccountPersistsWithoutSecondCommitCall(t *testing.T) {
	cfg, home, project, restoreLifecycle := setupSwitchStaleDestinationFixture(t)
	defer restoreLifecycle()
	const sid = "11111111-2222-3333-4444-555555555555"
	writeAccountTranscript(t, home, "personal", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-17T13:14:00.000Z","message":"hello"}`+"\n")

	storage := newTestStorage(t)
	inst := &Instance{ID: "switch-source", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusStopped, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))

	result, err := SwitchAccount(cfg, inst, "seminno", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)
	require.True(t, result.nativeResult.Committed)

	// Deliberately do NOT call CommitAccountSwitch: the historical bug is that
	// nothing else durably wrote the account, so re-reading storage right now
	// must already show the new account.
	stored, err := storage.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "seminno", stored[0].Account, "the account must be durable without a second caller-side commit step")
}

// Defect 2a: a destination transcript that install cannot advance as a byte
// prefix, but whose own newest event strictly predates the source's, is a
// stale pre-switch copy (exactly the personal-account leftover from 2026-09-10
// in the real incident) and must be archived automatically rather than
// blocking the switch.
func TestSwitchStaleDestination_OlderDestinationIsArchivedAutomatically(t *testing.T) {
	cfg, home, project, restoreLifecycle := setupSwitchStaleDestinationFixture(t)
	defer restoreLifecycle()
	const sid = "11111111-2222-3333-4444-555555555555"
	sourcePath := writeAccountTranscript(t, home, "personal", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-17T13:14:00.000Z","message":"current work"}`+"\n")
	destPath := writeAccountTranscript(t, home, "seminno", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-10T09:00:00.000Z","message":"stale pre-switch leftover"}`+"\n")

	storage := newTestStorage(t)
	inst := &Instance{ID: "switch-source", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusStopped, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))

	result, err := SwitchAccount(cfg, inst, "seminno", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err, "a stale destination must never block the switch")
	require.NotEmpty(t, result.DestinationArchived, "the stale destination must be archived, not silently dropped")
	require.Contains(t, result.DestinationArchived, ".pre-switch-")

	archived, err := os.ReadFile(result.DestinationArchived)
	require.NoError(t, err)
	require.Contains(t, string(archived), "stale pre-switch leftover", "the archived copy must preserve the old content")

	installed, err := os.ReadFile(destPath)
	require.NoError(t, err)
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.Equal(t, string(source), string(installed), "the source transcript must now be installed at the destination")
}

// Defect 2b: a destination that is provably newer (or whose staleness cannot
// be determined) must still be a bounded, recoverable refusal — never an
// automatic overwrite — but it must carry ErrSwitchDestinationDivergent so a
// caller can offer an explicit archive-and-retry action instead of a dead
// end, and that retry must succeed.
func TestSwitchStaleDestination_NewerDestinationRefusesThenArchivesOnRetry(t *testing.T) {
	cfg, home, project, restoreLifecycle := setupSwitchStaleDestinationFixture(t)
	defer restoreLifecycle()
	const sid = "11111111-2222-3333-4444-555555555555"
	writeAccountTranscript(t, home, "personal", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-10T09:00:00.000Z","message":"older source"}`+"\n")
	writeAccountTranscript(t, home, "seminno", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-17T13:14:00.000Z","message":"newer destination"}`+"\n")

	storage := newTestStorage(t)
	inst := &Instance{ID: "switch-source", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusStopped, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))

	_, err := SwitchAccount(cfg, inst, "seminno", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSwitchDestinationDivergent), "a genuinely newer destination must stay a refusal, not silently overwrite")
	require.Equal(t, "personal", inst.Account, "a refused switch must never leave the source mutated")

	// The failed attempt above must not block a fresh explicit retry (defect 3
	// covers this more directly; this exercises it through the public API).
	result, err := SwitchAccount(cfg, inst, "seminno", AccountSwitchOptions{Storage: storage, NoRestart: true, ArchiveDestination: true})
	require.NoError(t, err, "ArchiveDestination must make the retry succeed instead of a dead end")
	require.NotEmpty(t, result.DestinationArchived)
	require.Equal(t, "seminno", inst.Account)
}

// Defect 3: a failed switch journal is history, not a permanent blocker. The
// exact same request must be retriable, and the original failure must remain
// on disk (never deleted) instead of vanishing or blocking forever.
func TestSwitchStaleDestination_FailedJournalDoesNotBlockRetry(t *testing.T) {
	cfg, home, project, restoreLifecycle := setupSwitchStaleDestinationFixture(t)
	defer restoreLifecycle()
	const sid = "11111111-2222-3333-4444-555555555555"
	writeAccountTranscript(t, home, "personal", project, sid,
		`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-09-17T13:14:00.000Z","message":"hello"}`+"\n")

	inst := &Instance{ID: "switch-source", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusWaiting, CreatedAt: time.Now()}

	// Force the first attempt to fail after staging (simulating the target
	// failing to start) so a terminal "failed" journal is written for this
	// exact request generation. wasRunning must be true for the start path to
	// even run.
	nativeSwitchRunning = func(*Instance) bool { return true }
	nativeSwitchStart = func(*Instance) error { return errors.New("injected start failure") }
	_, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "seminno"}})
	require.Error(t, err)
	require.Equal(t, "personal", inst.Account, "a failed switch must restore the source account")

	journalPath, err := switchJournalPathForRequest(inst.ID, switchRequestGeneration(identityForInstance(inst), SwitchPreviewTarget{Harness: "claude", Account: "seminno"}, false))
	require.NoError(t, err)
	journal, err := loadSwitchJournal(journalPath)
	require.NoError(t, err)
	require.NotNil(t, journal)
	require.Equal(t, switchFailed, journal.State, "the first attempt must leave a terminal failed journal")
	require.Contains(t, journal.Failure, "injected start failure")

	// The exact same request must now succeed instead of "previous switch
	// failed" — archiving the failed journal happens as part of processing
	// this fresh attempt, not immediately after the failure.
	nativeSwitchStart = func(*Instance) error { return nil }
	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "seminno"}})
	require.NoError(t, err, "a failed journal must never block a fresh retry of the same request")
	require.True(t, result.Committed)
	require.Equal(t, "seminno", inst.Account)

	journal, err = loadSwitchJournal(journalPath)
	require.NoError(t, err)
	require.NotNil(t, journal, "the retry must have a fresh journal at the same request path")
	require.NotEqual(t, switchFailed, journal.State)

	matches, err := filepath.Glob(journalPath[:len(journalPath)-len(".json")] + ".failed-*.json")
	require.NoError(t, err)
	require.Len(t, matches, 1, "the original failure must be preserved as history, not deleted")
	historyJournal, err := loadSwitchJournal(matches[0])
	require.NoError(t, err)
	require.Equal(t, switchFailed, historyJournal.State)
	require.Contains(t, historyJournal.Failure, "injected start failure")
}
