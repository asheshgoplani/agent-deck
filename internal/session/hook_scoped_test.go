package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeHookStatusAt writes a minimal hook status file at an explicit path
// (scoped or flat), creating parent dirs as needed.
func writeHookStatusAt(t *testing.T, path, status, sessionID string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data := fmt.Sprintf(`{"status":%q,"session_id":%q,"event":"Stop","ts":%d}`,
		status, sessionID, time.Now().Unix())
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

// TestReadHookStatusFile_ResolvesScopedPath verifies that a sandboxed session's
// status file in the per-instance scoped subdir (…/hooks/sandbox/<id>/<id>.json)
// is resolved by readHookStatusFile.
func TestReadHookStatusFile_ResolvesScopedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	instanceID := "scoped-inst-1"
	scopedPath := filepath.Join(GetHooksDir(), "sandbox", instanceID, instanceID+".json")
	writeHookStatusAt(t, scopedPath, "waiting", "scoped-sess")

	hs := readHookStatusFile(instanceID)
	require.NotNil(t, hs, "scoped status file should be resolved")
	assert.Equal(t, "waiting", hs.Status)
	assert.Equal(t, "scoped-sess", hs.SessionID)
}

// TestReadHookStatusFile_FallsBackToFlat verifies that a non-sandbox session's
// flat status file (…/hooks/<id>.json) is still resolved when no scoped subdir
// exists — preserving existing behavior.
func TestReadHookStatusFile_FallsBackToFlat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	instanceID := "flat-inst-1"
	flatPath := filepath.Join(GetHooksDir(), instanceID+".json")
	writeHookStatusAt(t, flatPath, "running", "flat-sess")

	hs := readHookStatusFile(instanceID)
	require.NotNil(t, hs, "flat status file should be resolved when no scoped dir exists")
	assert.Equal(t, "running", hs.Status)
	assert.Equal(t, "flat-sess", hs.SessionID)
}

// TestReadHookStatusFile_PrefersScopedOverFlat verifies that when both a scoped
// and a flat file exist for the same instance, the scoped (container-bridged)
// file wins.
func TestReadHookStatusFile_PrefersScopedOverFlat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	instanceID := "both-inst-1"
	writeHookStatusAt(t, filepath.Join(GetHooksDir(), instanceID+".json"), "running", "flat-sess")
	writeHookStatusAt(t, filepath.Join(GetHooksDir(), "sandbox", instanceID, instanceID+".json"), "waiting", "scoped-sess")

	hs := readHookStatusFile(instanceID)
	require.NotNil(t, hs)
	assert.Equal(t, "waiting", hs.Status, "scoped path should take precedence over flat")
	assert.Equal(t, "scoped-sess", hs.SessionID)
}

// newTestWatcher constructs a StatusFileWatcher wired to a real fsnotify watcher
// rooted at the given hooksDir, suitable for live Start() tests.
func newTestWatcher(t *testing.T, hooksDir string) *StatusFileWatcher {
	t.Helper()
	fsw, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	return &StatusFileWatcher{
		hooksDir:   hooksDir,
		sandboxDir: filepath.Join(hooksDir, "sandbox"),
		watcher:    fsw,
		statuses:   make(map[string]*HookStatus),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// TestStatusFileWatcher_ObservesScopedWrite verifies that a live write into a
// per-instance scoped subdir that exists at startup is picked up by the watcher.
func TestStatusFileWatcher_ObservesScopedWrite(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	instanceID := "scoped-live-1"
	scopedDir := filepath.Join(hooksDir, "sandbox", instanceID)
	// Subdir exists before Start → exercises addExistingScopedWatches.
	require.NoError(t, os.MkdirAll(scopedDir, 0o700))

	w := newTestWatcher(t, hooksDir)
	go w.Start()
	defer w.Stop()

	// The status file is written AFTER the watcher is running → exercises the
	// live fsnotify path on the scoped subdir.
	writeHookStatusAt(t, filepath.Join(scopedDir, instanceID+".json"), "waiting", "live-sess")

	require.Eventually(t, func() bool {
		return w.GetHookStatus(instanceID) != nil
	}, 3*time.Second, 20*time.Millisecond, "watcher should observe scoped status write")
	assert.Equal(t, "waiting", w.GetHookStatus(instanceID).Status)
}

// TestStatusFileWatcher_ObservesNewScopedSubdir verifies that a per-instance
// scoped subdir created AFTER the watcher starts is dynamically watched and its
// status write observed — exercising the live Create-event handler that adds a
// watch on the new subdir and the loadDir backstop.
//
// The status file is written EXACTLY ONCE. An earlier version re-wrote it on
// every require.Eventually poll (25ms); because that interval is shorter than
// the watcher's 100ms event-debounce, each rewrite reset the debounce timer so
// it never fired, starving the live processFile path and leaving the assertion
// to race the one-shot loadDir in the create handler (~20-25% flake on macOS
// kqueue). A single write lets the debounce settle deterministically, and the
// loadDir-on-create backstop covers the write-before-watch ordering. The
// require.Eventually window is widened to suit kqueue create-event + debounce
// latency. (The transition daemon does not depend on this live watch at all —
// it reads the file directly via the poll/disk path — so this is the only path
// that needs the watcher to be live.)
func TestStatusFileWatcher_ObservesNewScopedSubdir(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	w := newTestWatcher(t, hooksDir)
	go w.Start()
	defer w.Stop()

	instanceID := "scoped-dyn-1"
	scopedDir := filepath.Join(hooksDir, "sandbox", instanceID)
	// Subdir is created AFTER Start → exercises the dynamic create-event handler
	// (watcher.Add on the new subdir + loadDir), distinct from the pre-created
	// sibling test TestStatusFileWatcher_ObservesScopedWrite.
	require.NoError(t, os.MkdirAll(scopedDir, 0o700))
	scopedFile := filepath.Join(scopedDir, instanceID+".json")
	writeHookStatusAt(t, scopedFile, "idle", "dyn-sess")

	require.Eventually(t, func() bool {
		return w.GetHookStatus(instanceID) != nil
	}, 10*time.Second, 50*time.Millisecond, "watcher should observe a write in a newly-created scoped subdir")
	assert.Equal(t, "idle", w.GetHookStatus(instanceID).Status)
}

// writeForgedHookStatusAt writes a status file carrying a forged terminal
// transition and an injected done_summary, at an explicit path.
func writeForgedHookStatusAt(t *testing.T, path, status, doneStatus, doneSummary string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// Future timestamp so a forged entry would look fresher than any legitimate
	// one and win the freshness/recency checks downstream.
	data := fmt.Sprintf(
		`{"status":%q,"session_id":"forged","event":"Stop","ts":%d,"done_status":%q,"done_summary":%q}`,
		status, time.Now().Add(time.Hour).Unix(), doneStatus, doneSummary)
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

// TestStatusFileWatcher_RejectsForeignNamedScopedFile is the PoC for the
// forge-sibling / inject-into-conductor vectors. A compromised container can
// write ANYWHERE inside its OWN per-instance subdir (…/hooks/sandbox/<id>/) and
// can name the file anything — including after a victim. The watcher must bind
// the instance ID to the OWNING SUBDIR, accept only <subdir>.json, and ignore
// any foreign-named file. This test fails on the pre-fix (filename-keyed) code,
// where statuses["victim-bbbb-222"] would be populated from the attacker's file.
func TestStatusFileWatcher_RejectsForeignNamedScopedFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	const (
		attackerID = "attacker-aaaa-111"
		victimID   = "victim-bbbb-222"
	)
	attackerDir := filepath.Join(hooksDir, "sandbox", attackerID)

	// (1) The attacker's own legitimate status file — allowed.
	writeHookStatusAt(t, filepath.Join(attackerDir, attackerID+".json"), "running", "attacker-sess")
	// (2) A forged file NAMED AFTER THE VICTIM, planted inside the attacker's
	//     own writable subdir. This is the exploit payload.
	writeForgedHookStatusAt(t, filepath.Join(attackerDir, victimID+".json"), "dead", "fail", "INJECTED")

	w := newTestWatcher(t, hooksDir)

	// Exercise the startup load path (loadExisting → loadScopedDirs → loadDir →
	// processFile).
	w.loadExisting()
	assert.Nil(t, w.GetHookStatus(victimID),
		"forged file named after a victim, inside the attacker's own subdir, must NOT be attributed to the victim (loadExisting)")
	require.NotNil(t, w.GetHookStatus(attackerID), "attacker's own status file should be ingested")
	assert.Equal(t, "running", w.GetHookStatus(attackerID).Status,
		"attacker's status must come ONLY from its own <id>.json, not the forged sibling file")

	// Exercise the overflow-recovery path (scanDisk → scanDirEntriesInto), which
	// rebuilds the whole map from disk.
	rebuilt := w.scanDisk()
	assert.Nil(t, rebuilt[victimID],
		"forged file must NOT appear in the scanDisk-rebuilt map (overflow recovery path)")
	require.NotNil(t, rebuilt[attackerID], "attacker's own status should appear in the rebuilt map")
	assert.Equal(t, "running", rebuilt[attackerID].Status)
}

// TestStatusFileWatcher_RejectsSymlinkedScopedFile is the PoC for the
// symlink-follow exfiltration/DoS vector. The per-instance subdir is
// container-writable, so a compromised container can make its OWN <id>.json a
// SYMLINK (its legit, attribution-passing name) pointing at a sibling/host file
// or /dev/zero. The host read path uses O_NOFOLLOW, so the symlink is refused
// rather than followed — the target's contents are never disclosed and the
// daemon never reads through to a device file. This test fails on the pre-fix
// (os.ReadFile, which follows symlinks) code, where statuses["<id>"] would be
// populated from the symlink target.
func TestStatusFileWatcher_RejectsSymlinkedScopedFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	const attackerID = "attacker-sym-111"
	attackerDir := filepath.Join(hooksDir, "sandbox", attackerID)
	require.NoError(t, os.MkdirAll(attackerDir, 0o700))

	// A "secret" host/sibling file the container should never be able to read
	// through. It carries a valid, fresh status so a successful follow WOULD be
	// ingested (proving the no-follow guard is what blocks it).
	secret := filepath.Join(tmpDir, "secret-host-file.json")
	require.NoError(t, os.WriteFile(secret,
		[]byte(`{"status":"dead","session_id":"leaked","event":"Stop","ts":9999999999}`), 0o644))

	// The container symlinks its OWN legit-named <id>.json at the secret file.
	link := filepath.Join(attackerDir, attackerID+".json")
	require.NoError(t, os.Symlink(secret, link))

	w := newTestWatcher(t, hooksDir)

	// Startup load path (loadExisting → loadScopedDirs → loadDir → processFile).
	w.loadExisting()
	assert.Nil(t, w.GetHookStatus(attackerID),
		"a symlinked <id>.json must NOT be followed (no host-file disclosure) on loadExisting")

	// Overflow-recovery path (scanDisk → scanDirEntriesInto).
	rebuilt := w.scanDisk()
	assert.Nil(t, rebuilt[attackerID],
		"a symlinked <id>.json must NOT be followed in the scanDisk-rebuilt map")

	// Direct: the no-follow reader itself refuses the symlink.
	_, err := readStatusFileNoFollow(link)
	require.Error(t, err, "readStatusFileNoFollow must error on a symlinked final component (O_NOFOLLOW)")
}

// TestStatusFileWatcher_RejectsOversizedScopedFile asserts the size bound: a
// huge real <id>.json (the OOM-the-notify-daemon DoS) is read bounded, not
// whole, and a status that only parses beyond the bound is therefore not
// ingested. Deterministic: the JSON's done_summary string is padded past the
// bound, so truncation cuts it mid-string and the parse fails.
func TestStatusFileWatcher_RejectsOversizedScopedFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	const instanceID = "oversized-222"
	scopedDir := filepath.Join(hooksDir, "sandbox", instanceID)
	require.NoError(t, os.MkdirAll(scopedDir, 0o700))

	// Valid JSON whose trailing string value runs well past maxHookStatusFileSize,
	// so a bounded read truncates mid-string → unterminated → unparseable.
	pad := strings.Repeat("A", maxHookStatusFileSize*2)
	huge := fmt.Sprintf(`{"status":"running","session_id":"s","event":"Stop","ts":1,"done_summary":%q}`, pad)
	scopedFile := filepath.Join(scopedDir, instanceID+".json")
	require.NoError(t, os.WriteFile(scopedFile, []byte(huge), 0o644))
	require.Greater(t, len(huge), maxHookStatusFileSize, "test fixture must exceed the bound")

	// The bounded reader returns at most maxHookStatusFileSize bytes, never the
	// whole file.
	data, err := readStatusFileNoFollow(scopedFile)
	require.NoError(t, err)
	assert.Len(t, data, maxHookStatusFileSize,
		"read must be capped at the bound, not read whole")
	assert.Less(t, len(data), len(huge), "bounded read must be shorter than the oversized file")

	// End to end: the truncated (now-invalid) JSON is not ingested.
	w := newTestWatcher(t, hooksDir)
	w.loadExisting()
	assert.Nil(t, w.GetHookStatus(instanceID),
		"an oversized status file whose payload only completes past the bound must not be ingested")
}

// TestHookStatusFilePath_IgnoresSymlinkedScopedPath asserts the read-side
// resolver does not PREFER (nor follow) a symlinked scoped path: hookStatusFilePath
// Lstats the scoped path, so a symlink there is skipped in favour of the flat
// path, and readStatusFileNoFollow would refuse to follow it anyway.
func TestHookStatusFilePath_IgnoresSymlinkedScopedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const instanceID = "sym-resolve-1"
	// Legitimate flat status for this id.
	flatPath := filepath.Join(GetHooksDir(), instanceID+".json")
	writeHookStatusAt(t, flatPath, "running", "flat-sess")

	// A secret file the container would like disclosed via a scoped symlink.
	secret := filepath.Join(t.TempDir(), "secret.json")
	writeHookStatusAt(t, secret, "waiting", "leaked-sess")

	scopedDir := filepath.Join(GetHooksDir(), "sandbox", instanceID)
	require.NoError(t, os.MkdirAll(scopedDir, 0o700))
	require.NoError(t, os.Symlink(secret, filepath.Join(scopedDir, instanceID+".json")))

	// Scoped path is a symlink → not preferred; flat path wins and the symlink
	// target ("waiting"/"leaked-sess") is never read.
	assert.Equal(t, flatPath, hookStatusFilePath(instanceID),
		"a symlinked scoped path must not be preferred over the flat path")
	hs := readHookStatusFile(instanceID)
	require.NotNil(t, hs)
	assert.Equal(t, "running", hs.Status, "must read the flat file, not the symlink target")
	assert.NotEqual(t, "leaked-sess", hs.SessionID, "symlink target must not be disclosed")
}

// TestStatusFileWatcher_FlatDirStillFilenameKeyed verifies the non-sandbox flat
// top-level dir keeps its existing filename-keying so ordinary (non-sandbox)
// sessions are unaffected by the scope-binding fix.
func TestStatusFileWatcher_FlatDirStillFilenameKeyed(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	writeHookStatusAt(t, filepath.Join(hooksDir, "flat-inst-9.json"), "waiting", "flat-sess")

	w := newTestWatcher(t, hooksDir)
	w.loadExisting()

	require.NotNil(t, w.GetHookStatus("flat-inst-9"), "flat top-level status file should be ingested by filename")
	assert.Equal(t, "waiting", w.GetHookStatus("flat-inst-9").Status)
}
