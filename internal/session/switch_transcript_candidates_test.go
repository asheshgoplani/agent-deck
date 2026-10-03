// Regression coverage for the stale-transcript account switch found on
// 2026-10-02: Claude Code keys one working directory under up to three
// project directories (the path as typed, its macOS /private form and its
// realpath), and the switch only ever looked at one of them in one account.
// The switch now gathers every copy in the source AND the destination
// account, keeps the newest (tie: longest), installs it under every key the
// destination harness could resume from, and backs up whatever it replaces.
package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// transcriptBody renders n timestamped JSONL events whose newest event is
// `newest`. Every event carries a distinct body so files of different length
// never share a byte prefix.
func transcriptBody(sid string, newest time.Time, n int, tag string) string {
	var b strings.Builder
	for i := n - 1; i >= 0; i-- {
		ts := newest.Add(-time.Duration(i) * time.Minute).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(&b, `{"sessionId":%q,"type":"user","timestamp":%q,"message":"%s %d"}`+"\n", sid, ts, tag, n-i)
	}
	return b.String()
}

// switchKeysFixture wires two accounts and a project reached through a
// symlink, so the typed path and the realpath encode to different Claude
// project keys (on macOS t.TempDir() adds a /private realpath on top).
type switchKeysFixture struct {
	cfg              *UserConfig
	home             string
	typedProject     string
	realProject      string
	keys             []string
	restoreLifecycle func()
}

func newSwitchKeysFixture(t *testing.T) *switchKeysFixture {
	t.Helper()
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.work.claude]
config_dir = "~/.claude-work"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)

	realProject := filepath.Join(home, "real", "project")
	require.NoError(t, os.MkdirAll(realProject, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "link"), 0o700))
	typedProject := filepath.Join(home, "link", "project")
	require.NoError(t, os.Symlink(realProject, typedProject))

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	nativeSwitchRunning = func(*Instance) bool { return false }
	nativeSwitchStop = func(*Instance) error { return nil }
	nativeSwitchStart = func(*Instance) error { return nil }
	f := &switchKeysFixture{cfg: cfg, home: home, typedProject: typedProject, realProject: realProject,
		keys: claudeProjectKeyCandidates(typedProject),
		restoreLifecycle: func() {
			nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
		}}
	t.Cleanup(f.restoreLifecycle)
	require.GreaterOrEqual(t, len(f.keys), 2, "a symlinked cwd must yield at least the typed and the realpath key")
	return f
}

func (f *switchKeysFixture) path(account, key, sid string) string {
	return filepath.Join(f.home, ".claude-"+account, "projects", key, sid+".jsonl")
}

func (f *switchKeysFixture) write(t *testing.T, account, key, sid, body string) string {
	t.Helper()
	p := f.path(account, key, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func (f *switchKeysFixture) instance(t *testing.T, sid string) (*Instance, *Storage) {
	t.Helper()
	storage := newTestStorage(t)
	inst := &Instance{ID: "switch-keys", Title: "keys", ProjectPath: f.typedProject, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusStopped, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))
	return inst, storage
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err, p)
	return string(b)
}

func (f *switchKeysFixture) requireInstalledEverywhere(t *testing.T, account, sid, want string) {
	t.Helper()
	for _, key := range f.keys {
		require.Equal(t, want, readFile(t, f.path(account, key, sid)), "destination key %s must hold the chosen transcript", key)
	}
}

const keysSID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

var (
	octOne = time.Date(2026, 10, 1, 13, 30, 0, 0, time.UTC)
	octTwo = time.Date(2026, 10, 2, 17, 45, 0, 0, time.UTC)
)

// claudeProjectKeyCandidates is the single place that knows how Claude Code
// may key one working directory. The order is typed, /private form, realpath,
// deduplicated, and the macOS /private form is only added on darwin.
func TestClaudeProjectKeyCandidates(t *testing.T) {
	resolve := func(p string) (string, error) {
		switch p {
		case "/tmp/exec-x", "/private/tmp/exec-x":
			return "/Users/u/agent-deck-work/exec-x", nil
		case "/Users/u/plain":
			return "/Users/u/plain", nil
		}
		return "", os.ErrNotExist
	}
	got := claudeProjectKeyCandidatesWith("/tmp/exec-x", "darwin", resolve)
	require.Equal(t, []string{"-tmp-exec-x", "-private-tmp-exec-x", "-Users-u-agent-deck-work-exec-x"}, got)

	got = claudeProjectKeyCandidatesWith("/tmp/exec-x", "linux", resolve)
	require.Equal(t, []string{"-tmp-exec-x", "-Users-u-agent-deck-work-exec-x"}, got, "no /private form outside darwin")

	got = claudeProjectKeyCandidatesWith("/Users/u/plain", "darwin", resolve)
	require.Equal(t, []string{"-Users-u-plain"}, got, "a plain path yields one key")

	got = claudeProjectKeyCandidatesWith("/private/tmp/exec-x", "darwin", resolve)
	require.Equal(t, []string{"-private-tmp-exec-x", "-tmp-exec-x", "-Users-u-agent-deck-work-exec-x"}, got, "a typed /private path also yields its bare form")

	got = claudeProjectKeyCandidatesWith("/gone/dir", "darwin", resolve)
	require.Equal(t, []string{"-gone-dir"}, got, "an unresolvable path still yields its typed key")

	require.Nil(t, claudeProjectKeyCandidatesWith("", "darwin", resolve))
}

// Incident (a), 2026-10-02: the destination already held a day-old copy under
// the same key. The newer source must replace it, the old copy must survive
// as a backup, and the result must say what was chosen.
func TestSwitchTranscript_StaleSameKeyDestinationIsReplacedAndBackedUp(t *testing.T) {
	f := newSwitchKeysFixture(t)
	newest := transcriptBody(keysSID, octTwo, 12, "today")
	stale := transcriptBody(keysSID, octOne, 6, "yesterday")
	f.write(t, "personal", f.keys[0], keysSID, newest)
	destPath := f.write(t, "work", f.keys[0], keysSID, stale)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)

	f.requireInstalledEverywhere(t, "work", keysSID, newest)
	require.NotNil(t, result.Transcript)
	require.Equal(t, f.path("personal", f.keys[0], keysSID), result.Transcript.Chosen.Path)
	require.Equal(t, "source", result.Transcript.Chosen.Side)
	require.True(t, result.Transcript.Chosen.NewestEvent.Equal(octTwo))
	require.Equal(t, 12, result.Transcript.Chosen.Lines)
	require.Len(t, result.Transcript.BackedUp, 1, "exactly the stale destination copy is backed up")
	require.Equal(t, stale, readFile(t, result.Transcript.BackedUp[0]), "the backup must preserve the replaced bytes")
	require.True(t, strings.HasPrefix(result.Transcript.BackedUp[0], destPath+"."), "backup sits next to the replaced copy")
	require.Contains(t, result.Conversation, "chose")
	require.Contains(t, result.Conversation, "newest event 2026-10-02T17:45:00Z")
	require.Contains(t, result.Conversation, fmt.Sprintf("installed under %d project key", len(f.keys)))
}

// Incident (b), 2026-10-02: the session's cwd is a symlink. The source held a
// short stale copy under the typed key and the real 24k-line conversation
// under the realpath key; the destination held another stale copy under the
// realpath key. The newest copy must win and land under every key.
func TestSwitchTranscript_SymlinkedCwdPicksNewestAcrossKeysAndAccounts(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey, realKey := f.keys[0], f.keys[len(f.keys)-1]
	require.NotEqual(t, typedKey, realKey)

	sourceStale := transcriptBody(keysSID, octOne.Add(-2*time.Hour), 5, "typed-stale")
	sourceNewest := transcriptBody(keysSID, octTwo, 24, "real-newest")
	destStale := transcriptBody(keysSID, octOne, 9, "dest-stale")
	f.write(t, "personal", typedKey, keysSID, sourceStale)
	newestPath := f.write(t, "personal", realKey, keysSID, sourceNewest)
	f.write(t, "work", realKey, keysSID, destStale)

	// A sidecar directory next to the newest copy travels with it.
	sidecar := filepath.Join(filepath.Dir(newestPath), keysSID)
	require.NoError(t, os.MkdirAll(sidecar, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sidecar, "agent-1.jsonl"), []byte(`{"type":"sidechain"}`+"\n"), 0o600))

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)

	f.requireInstalledEverywhere(t, "work", keysSID, sourceNewest)
	require.Equal(t, newestPath, result.Transcript.Chosen.Path)
	require.Equal(t, 24, result.Transcript.Chosen.Lines)
	require.Len(t, result.Transcript.Candidates, 3, "both source copies and the destination copy are considered")
	require.Len(t, result.Transcript.BackedUp, 1)
	require.Equal(t, destStale, readFile(t, result.Transcript.BackedUp[0]))
	for _, key := range f.keys {
		require.FileExists(t, filepath.Join(f.home, ".claude-work", "projects", key, keysSID, "agent-1.jsonl"), "sidecar must follow under key %s", key)
	}
	// Copy-only: the source account keeps both of its copies untouched.
	require.Equal(t, sourceStale, readFile(t, f.path("personal", typedKey, keysSID)))
	require.Equal(t, sourceNewest, readFile(t, newestPath))
	require.Equal(t, "work", inst.Account)
}

// Learning 20260930-002: the source held its transcript only under the
// realpath key. The switch used to report "exact source preflight failed" or
// "no conversation to migrate"; it must migrate.
func TestSwitchTranscript_SourceOnlyUnderRealpathKeyMigrates(t *testing.T) {
	f := newSwitchKeysFixture(t)
	realKey := f.keys[len(f.keys)-1]
	body := transcriptBody(keysSID, octTwo, 7, "real-only")
	only := f.write(t, "personal", realKey, keysSID, body)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)
	f.requireInstalledEverywhere(t, "work", keysSID, body)
	require.Equal(t, only, result.Transcript.Chosen.Path)
	require.Empty(t, result.Transcript.BackedUp)
	require.NotContains(t, result.Conversation, "no conversation to migrate")
}

// A destination copy that is newer than every source copy is the real
// conversation (the user worked there last). It must never be overwritten by
// the older source; it is kept and propagated to the other keys instead.
func TestSwitchTranscript_NewerDestinationWinsAndIsNeverOverwritten(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	older := transcriptBody(keysSID, octOne, 8, "older-source")
	newer := transcriptBody(keysSID, octTwo, 15, "newer-destination")
	f.write(t, "personal", typedKey, keysSID, older)
	destPath := f.write(t, "work", typedKey, keysSID, newer)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err, "a newer destination is kept, not a refusal")
	require.False(t, errors.Is(err, ErrSwitchDestinationDivergent))

	require.Equal(t, newer, readFile(t, destPath), "the newer destination bytes are untouched")
	f.requireInstalledEverywhere(t, "work", keysSID, newer)
	require.Equal(t, destPath, result.Transcript.Chosen.Path)
	require.Equal(t, "destination", result.Transcript.Chosen.Side)
	require.Empty(t, result.Transcript.BackedUp, "nothing was replaced, so nothing is backed up")
	require.Equal(t, older, readFile(t, f.path("personal", typedKey, keysSID)), "the source copy is left alone")
	require.Equal(t, "work", inst.Account)
	require.Contains(t, result.Conversation, "destination")
}

// --archive-destination keeps its meaning: the caller insists on the source
// copy even though the destination is newer. The newer copy is archived, never
// deleted.
func TestSwitchTranscript_ArchiveDestinationForcesTheSourceCopy(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	older := transcriptBody(keysSID, octOne, 8, "older-source")
	newer := transcriptBody(keysSID, octTwo, 15, "newer-destination")
	f.write(t, "personal", typedKey, keysSID, older)
	destPath := f.write(t, "work", typedKey, keysSID, newer)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true, ArchiveDestination: true})
	require.NoError(t, err)
	f.requireInstalledEverywhere(t, "work", keysSID, older)
	require.Equal(t, "source", result.Transcript.Chosen.Side)
	require.Len(t, result.Transcript.BackedUp, 1)
	require.Equal(t, newer, readFile(t, result.Transcript.BackedUp[0]))
	require.NotEmpty(t, result.DestinationArchived)
	require.True(t, strings.HasPrefix(result.DestinationArchived, destPath+".pre-switch-"))
}

// Equal newest timestamps: the longer transcript carries more of the
// conversation and wins.
func TestSwitchTranscript_TieOnTimestampPicksTheLongest(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	short := transcriptBody(keysSID, octTwo, 4, "short")
	long := transcriptBody(keysSID, octTwo, 9, "long")
	f.write(t, "personal", typedKey, keysSID, short)
	destPath := f.write(t, "work", typedKey, keysSID, long)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)
	require.Equal(t, destPath, result.Transcript.Chosen.Path)
	f.requireInstalledEverywhere(t, "work", keysSID, long)
	require.Empty(t, result.Transcript.BackedUp)
}

// A destination copy whose age cannot be read (no timestamped events) and
// which differs from the chosen copy is never overwritten by default; the
// switch refuses before writing anything, and --archive-destination is the
// explicit way through.
func TestSwitchTranscript_UndatedDifferingDestinationRefusesUntilArchived(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	source := transcriptBody(keysSID, octTwo, 3, "dated-source")
	undated := `{"sessionId":"` + keysSID + `","type":"user","message":"no timestamp here"}` + "\n"
	f.write(t, "personal", typedKey, keysSID, source)
	destPath := f.write(t, "work", typedKey, keysSID, undated)

	inst, storage := f.instance(t, keysSID)
	_, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSwitchDestinationDivergent), "got %v", err)
	require.Equal(t, undated, readFile(t, destPath), "nothing may be written on refusal")
	require.Equal(t, "personal", inst.Account)
	for _, key := range f.keys[1:] {
		require.NoFileExists(t, f.path("work", key, keysSID), "no other key may be populated on refusal")
	}

	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true, ArchiveDestination: true})
	require.NoError(t, err)
	f.requireInstalledEverywhere(t, "work", keysSID, source)
	require.Len(t, result.Transcript.BackedUp, 1)
	require.Equal(t, undated, readFile(t, result.Transcript.BackedUp[0]))
}

// Identical copies on both sides are a no-op install: nothing is backed up
// and every key still ends up populated.
func TestSwitchTranscript_IdenticalCopiesNeedNoBackup(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	body := transcriptBody(keysSID, octTwo, 5, "same")
	f.write(t, "personal", typedKey, keysSID, body)
	f.write(t, "work", typedKey, keysSID, body)

	inst, storage := f.instance(t, keysSID)
	result, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err)
	f.requireInstalledEverywhere(t, "work", keysSID, body)
	require.Empty(t, result.Transcript.BackedUp)
	require.Len(t, result.Transcript.Installed, len(f.keys)-1, "only the keys that were empty are new installs")
}

// The context exporter (handoff, cross-harness switch) resolves the same
// multi-key set and exports the newest copy instead of failing on a symlinked
// cwd whose transcript lives under the realpath key.
func TestExportClaudeContext_FindsNewestCopyAcrossProjectKeys(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey, realKey := f.keys[0], f.keys[len(f.keys)-1]
	f.write(t, "personal", typedKey, keysSID, transcriptBody(keysSID, octOne, 2, "old"))
	newestPath := f.write(t, "personal", realKey, keysSID, transcriptBody(keysSID, octTwo, 6, "new"))

	inst, _ := f.instance(t, keysSID)
	export, err := ExportClaudeContext(inst, 0)
	require.NoError(t, err)
	require.Equal(t, newestPath, export.Manifest.Artifact.Path)
}

// A refusal raised before the source is stopped must leave a failed journal,
// not a "prepared" one: otherwise the next switch of the same session (to any
// account) is refused as an unresolved prior operation.
func TestSwitchTranscript_PreStopRefusalDoesNotBlockLaterSwitch(t *testing.T) {
	f := newSwitchKeysFixture(t)
	typedKey := f.keys[0]
	source := transcriptBody(keysSID, octTwo, 3, "dated-source")
	undated := `{"sessionId":"` + keysSID + `","type":"user","message":"no timestamp here"}` + "\n"
	f.write(t, "personal", typedKey, keysSID, source)
	f.write(t, "work", typedKey, keysSID, undated)

	inst, storage := f.instance(t, keysSID)
	_, err := SwitchAccount(f.cfg, inst, "work", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.True(t, errors.Is(err, ErrSwitchDestinationDivergent), "got %v", err)

	// A third account with no copy at all must now be reachable.
	third := filepath.Join(f.home, ".claude-third")
	require.NoError(t, os.MkdirAll(third, 0o700))
	cfgPath := filepath.Join(f.home, ".agent-deck", "config.toml")
	body, readErr := os.ReadFile(cfgPath)
	require.NoError(t, readErr)
	require.NoError(t, os.WriteFile(cfgPath, append(body, []byte("[profiles.third.claude]\nconfig_dir = \"~/.claude-third\"\n")...), 0o600))
	ClearUserConfigCache()
	cfg, loadErr := LoadUserConfig()
	require.NoError(t, loadErr)

	result, err := SwitchAccount(cfg, inst, "third", AccountSwitchOptions{Storage: storage, NoRestart: true})
	require.NoError(t, err, "a pre-stop refusal must not leave an unresolved journal behind")
	require.Equal(t, "third", inst.Account)
	require.Equal(t, source, readFile(t, f.path("third", typedKey, keysSID)))
	require.Equal(t, "source", result.Transcript.Chosen.Side)
}
