package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// seedStoreCLI creates profiles/<profile>/state.db under root with n rows,
// directly through statedb so both data roots can be shaped independently of
// the resolver under test.
func seedStoreCLI(t *testing.T, root, profile string, n int) string {
	t.Helper()
	dbPath := filepath.Join(root, session.ProfilesDirName, profile, "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 0; i < n; i++ {
		row := &statedb.InstanceRow{
			ID:           profile + "-seed-" + string(rune('a'+i)),
			Title:        "legacy-" + string(rune('a'+i)),
			ProjectPath:  root,
			Tool:         "shell",
			Status:       "idle",
			CreatedAt:    time.Now(),
			LastAccessed: time.Now(),
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	return dbPath
}

// strayXDGFixture is the 2026-09-19/20 incident on disk: a populated legacy
// store and an EMPTY XDG store for the same profile. The CLI helper points
// XDG_DATA_HOME at $HOME/.local/share and selects profile ch_support_test.
func strayXDGFixture(t *testing.T) (home, legacyDB, strayDB string) {
	t.Helper()
	home = t.TempDir()
	legacyRoot := filepath.Join(home, ".agent-deck")
	xdgRoot := filepath.Join(home, ".local", "share", "agent-deck")
	legacyDB = seedStoreCLI(t, legacyRoot, "ch_support_test", 3)
	strayDB = seedStoreCLI(t, xdgRoot, "ch_support_test", 0)
	return home, legacyDB, strayDB
}

func fileSignature(t *testing.T, path string) string {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.ModTime().String() + "/" + strconv.FormatInt(st.Size(), 10)
}

// TestCLI_StrayEmptyXDGStore_NewProcessesKeepLegacy is the incident from the
// CLI's point of view: with an empty XDG store beside the populated legacy
// one, a fresh `list` process must still show every session, and neither
// `list` nor the hook handler may touch the stray or create anything under
// the XDG profiles dir.
func TestCLI_StrayEmptyXDGStore_NewProcessesKeepLegacy(t *testing.T) {
	home, _, strayDB := strayXDGFixture(t)
	before := fileSignature(t, strayDB)

	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list exit %d: %s %s", code, stdout, stderr)
	}
	if got := strings.Count(stdout, `"legacy-`); got != 3 {
		t.Fatalf("list saw %d of the 3 legacy sessions (the stray XDG store was preferred?):\n%s", got, stdout)
	}

	// The hook handler is the status writer that went blind during the
	// incident; it runs as a fresh process per Claude Code hook event.
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"` + home + `"}`
	_, stderr, code = runAgentDeckEnv(t, home, payload, []string{"AGENTDECK_INSTANCE_ID=ch_support_test-seed-a"}, "hook-handler")
	if code != 0 {
		t.Fatalf("hook-handler exit %d: %s", code, stderr)
	}

	if after := fileSignature(t, strayDB); after != before {
		t.Fatalf("stray XDG store was modified by a CLI process: %s -> %s", before, after)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".local", "share", "agent-deck", session.ProfilesDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("XDG profiles dir gained entries: %v", entries)
	}
}

// TestCLI_Doctor_ReportsStoreDivergence checks the operator surface: doctor
// names both roots, their session counts, the active one and a WARNING, in
// text and JSON, without changing HOME.
func TestCLI_Doctor_ReportsStoreDivergence(t *testing.T) {
	home, legacyDB, strayDB := strayXDGFixture(t)
	legacyRoot := filepath.Dir(filepath.Dir(filepath.Dir(legacyDB)))
	xdgRoot := filepath.Dir(filepath.Dir(filepath.Dir(strayDB)))
	before := snapshotTree(t, home)

	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	for _, want := range []string{
		"Profile store: " + legacyRoot + " (legacy, reason=stray_xdg_store)",
		legacyRoot + ": 3 sessions (ch_support_test=3) [active]",
		xdgRoot + ": 0 sessions (ch_support_test=0)",
		"WARNING: stray empty XDG profile store at " + filepath.Join(xdgRoot, session.ProfilesDirName),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, code = runAgentDeck(t, home, "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor --json exit %d: %s %s", code, stdout, stderr)
	}
	var report struct {
		StoreRoots session.StoreRootSelection `json:"store_roots"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor JSON: %v: %s", err, stdout)
	}
	sel := report.StoreRoots
	if sel.Active != legacyRoot || sel.Kind != "legacy" || sel.Reason != session.StoreRootReasonStrayXDG || !sel.Divergent {
		t.Fatalf("store_roots = %+v", sel)
	}
	if sel.Legacy.Sessions != 3 || sel.XDG.Sessions != 0 || sel.XDG.Profiles["ch_support_test"] != 0 {
		t.Fatalf("store_roots counts = legacy %+v xdg %+v", sel.Legacy, sel.XDG)
	}

	stdout, stderr, code = runAgentDeck(t, home, "health")
	if code != 0 {
		t.Fatalf("health exit %d: %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "profile store divergence: stray empty XDG profile store") {
		t.Fatalf("health output lacks the divergence flag:\n%s", stdout)
	}

	// Read-only diagnostics create no new files under HOME. The read-only
	// live open of each store leaves SQLite's -shm/-wal sidecars behind
	// (and bumps the profile dir's mtime), so compare paths, not contents.
	seen := snapshotPaths(before)
	for path := range snapshotPaths(snapshotTree(t, home)) {
		if seen[path] || strings.HasSuffix(path, "-shm") || strings.HasSuffix(path, "-wal") {
			continue
		}
		t.Errorf("doctor/health created %s", path)
	}
}

// snapshotPaths reduces a snapshotTree to its relative paths (the key's
// leading field, before " mode=").
func snapshotPaths(snapshot map[string]bool) map[string]bool {
	paths := make(map[string]bool, len(snapshot))
	for key := range snapshot {
		paths[strings.SplitN(key, " mode=", 2)[0]] = true
	}
	return paths
}

// TestCLI_Doctor_CleanLayoutHasNoStoreWarning: a single-root layout prints
// the active store and no WARNING line.
func TestCLI_Doctor_CleanLayoutHasNoStoreWarning(t *testing.T) {
	home := t.TempDir()
	legacyRoot := filepath.Join(home, ".agent-deck")
	seedStoreCLI(t, legacyRoot, "ch_support_test", 2)

	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Profile store: "+legacyRoot+" (legacy, reason=legacy_only)") {
		t.Fatalf("doctor output lacks the active store line:\n%s", stdout)
	}
	if !strings.Contains(stdout, legacyRoot+": 2 sessions (ch_support_test=2) [active]") {
		t.Fatalf("doctor counts the clean layout wrong:\n%s", stdout)
	}
	if strings.Contains(stdout, "stray") || strings.Contains(stdout, "profile stores exist under both") {
		t.Fatalf("clean layout must not warn:\n%s", stdout)
	}
}

// TestStoreSelection_LogsOnce checks the structured lines: one
// `store_selected` per process for the decision and a WARN
// `stray_xdg_store` naming the stray path, emitted by LogStoreRootSelection
// after logging is up, however many times the resolver ran before.
func TestStoreSelection_LogsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ResetStoreRootSelection()
	t.Cleanup(session.ResetStoreRootSelection)
	legacyRoot := filepath.Join(home, ".agent-deck")
	xdgRoot := filepath.Join(home, ".local", "share", "agent-deck")
	seedStoreCLI(t, legacyRoot, "personal", 2)
	seedStoreCLI(t, xdgRoot, "personal", 0)

	// The resolvers run before logging.Init, as in every real process.
	for i := 0; i < 3; i++ {
		if _, err := session.SelectStoreRoot(); err != nil {
			t.Fatal(err)
		}
	}
	logDir := initTestLogging(t)
	for i := 0; i < 3; i++ {
		session.LogStoreRootSelection()
	}
	body := readLogTolerant(t, logDir)
	if got := strings.Count(body, `"store_selected"`); got != 1 {
		t.Fatalf("store_selected logged %d times, want once; log:\n%s", got, body)
	}
	if !strings.Contains(body, `"path":"`+legacyRoot+`"`) || !strings.Contains(body, `"reason":"stray_xdg_store"`) {
		t.Fatalf("store_selected lacks path/reason; log:\n%s", body)
	}
	if got := strings.Count(body, `"msg":"stray_xdg_store"`); got != 1 {
		t.Fatalf("stray_xdg_store WARN logged %d times, want once; log:\n%s", got, body)
	}
	if !strings.Contains(body, `"level":"WARN"`) || !strings.Contains(body, filepath.Join(xdgRoot, session.ProfilesDirName)) {
		t.Fatalf("stray_xdg_store WARN lacks level/path; log:\n%s", body)
	}
}

// TestCLI_StrayLayout_WarnsOnceOnStderr: a CLI process (which never opens
// the debug log) prints the layout WARNING exactly once on stderr; the hook
// handler and doctor (which reports it on stdout) stay silent on stderr.
func TestCLI_StrayLayout_WarnsOnceOnStderr(t *testing.T) {
	home, _, strayDB := strayXDGFixture(t)
	xdgProfiles := filepath.Dir(filepath.Dir(strayDB))

	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list exit %d: %s %s", code, stdout, stderr)
	}
	if got := strings.Count(stderr, "WARNING: stray empty XDG profile store at "+xdgProfiles); got != 1 {
		t.Fatalf("list stderr carries the WARNING %d times, want once:\n%s", got, stderr)
	}
	if !strings.Contains(stderr, "migrate-paths") {
		t.Fatalf("WARNING names no remedy:\n%s", stderr)
	}

	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"` + home + `"}`
	_, stderr, code = runAgentDeckEnv(t, home, payload, []string{"AGENTDECK_INSTANCE_ID=ch_support_test-seed-a"}, "hook-handler")
	if code != 0 || strings.Contains(stderr, "WARNING") {
		t.Fatalf("hook-handler exit %d, stderr must stay silent:\n%s", code, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "doctor")
	if code != 0 || strings.Contains(stderr, "WARNING: stray") || !strings.Contains(stdout, "WARNING: stray") {
		t.Fatalf("doctor exit %d: WARNING belongs on stdout only\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// A clean layout is silent.
	clean := t.TempDir()
	seedStoreCLI(t, filepath.Join(clean, ".agent-deck"), "ch_support_test", 1)
	if _, stderr, code = runAgentDeck(t, clean, "list"); code != 0 || strings.Contains(stderr, "WARNING") {
		t.Fatalf("clean layout list exit %d stderr:\n%s", code, stderr)
	}
}

// doctorStoreRoots runs `doctor --json` and returns its store_roots block.
func doctorStoreRoots(t *testing.T, home string) session.StoreRootSelection {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor --json exit %d: %s %s", code, stdout, stderr)
	}
	var report struct {
		StoreRoots session.StoreRootSelection `json:"store_roots"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor JSON: %v: %s", err, stdout)
	}
	return report.StoreRoots
}

// listIDs returns the session ids `list --json` prints.
func listIDs(t *testing.T, home string, extra ...string) []string {
	t.Helper()
	args := append(append([]string{}, extra...), "list", "--json")
	stdout, stderr, code := runAgentDeck(t, home, args...)
	if code != 0 {
		t.Fatalf("list exit %d: %s %s", code, stdout, stderr)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("list JSON: %v: %s", err, stdout)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// TestCLI_MigratePaths_PinsXDGAcrossRemoveAndNewProfile is the review's P1
// end to end: legacy -> `migrate-paths` -> the CLI uses XDG; removing
// sessions and creating a profile afterwards keeps every fresh process on
// XDG (the legacy copy, still populated, never wins again) and the new
// profile reachable.
func TestCLI_MigratePaths_PinsXDGAcrossRemoveAndNewProfile(t *testing.T) {
	home := t.TempDir()
	legacyRoot := filepath.Join(home, ".agent-deck")
	xdgRoot := filepath.Join(home, ".local", "share", "agent-deck")
	seedStoreCLI(t, legacyRoot, "ch_support_test", 3)

	stdout, stderr, code := runAgentDeck(t, home, "migrate-paths")
	if code != 0 {
		t.Fatalf("migrate-paths exit %d: %s %s", code, stdout, stderr)
	}
	marker := filepath.Join(xdgRoot, session.ProfilesDirName, session.StoreRootMarkerName)
	if !strings.Contains(stdout, "active profile store root pinned to XDG: "+marker) {
		t.Fatalf("migrate-paths did not report the pin:\n%s", stdout)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	if sel := doctorStoreRoots(t, home); sel.Kind != "xdg" || sel.Reason != session.StoreRootReasonMarker || sel.XDG.Sessions != 3 {
		t.Fatalf("after migrate-paths store_roots = %+v", sel)
	}
	ids := listIDs(t, home)
	if len(ids) != 3 {
		t.Fatalf("list after migration = %v", ids)
	}

	for _, id := range ids[:2] {
		if stdout, stderr, code = runAgentDeck(t, home, "remove", id); code != 0 {
			t.Fatalf("remove %s exit %d: %s %s", id, code, stdout, stderr)
		}
	}
	if stdout, stderr, code = runAgentDeck(t, home, "profile", "create", "work"); code != 0 {
		t.Fatalf("profile create exit %d: %s %s", code, stdout, stderr)
	}

	// Fresh processes: XDG (1 row) stays active over legacy (3 rows).
	sel := doctorStoreRoots(t, home)
	if sel.Kind != "xdg" || sel.Reason != session.StoreRootReasonMarker {
		t.Fatalf("post-remove store_roots flipped: %+v", sel)
	}
	if sel.Legacy.Sessions != 3 || sel.XDG.Profiles["ch_support_test"] != 1 {
		t.Fatalf("post-remove counts: legacy %+v xdg %+v", sel.Legacy, sel.XDG)
	}
	if got := listIDs(t, home); len(got) != 1 {
		t.Fatalf("removed sessions came back: %v", got)
	}
	if stdout, stderr, code = runAgentDeck(t, home, "-p", "work", "list"); code != 0 {
		t.Fatalf("-p work list exit %d: %s %s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, session.ProfilesDirName, "work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work profile leaked into the legacy root: %v", err)
	}
	if _, stderr, code = runAgentDeck(t, home, "list"); code != 0 || strings.Contains(stderr, "WARNING") {
		t.Fatalf("migrated layout must not warn (exit %d):\n%s", code, stderr)
	}
}

// TestCLI_MigratePaths_SetsAsideStrayThenMigrates: the remedy the stray
// WARNING names has to work on the stray layout itself: the empty XDG
// profiles/ is set aside, the legacy stores copied, XDG pinned.
func TestCLI_MigratePaths_SetsAsideStrayThenMigrates(t *testing.T) {
	home, _, strayDB := strayXDGFixture(t)
	xdgProfiles := filepath.Dir(filepath.Dir(strayDB))

	stdout, stderr, code := runAgentDeck(t, home, "migrate-paths")
	if code != 0 {
		t.Fatalf("migrate-paths exit %d: %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "set aside empty stray data profiles -> "+xdgProfiles+".stray-") {
		t.Fatalf("stray not set aside:\n%s", stdout)
	}
	entries, err := filepath.Glob(xdgProfiles + ".stray-*")
	if err != nil || len(entries) != 1 {
		t.Fatalf("stray copies = %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(entries[0], "ch_support_test", "state.db")); err != nil {
		t.Fatalf("stray store not preserved: %v", err)
	}
	if sel := doctorStoreRoots(t, home); sel.Kind != "xdg" || sel.Reason != session.StoreRootReasonMarker || sel.XDG.Profiles["ch_support_test"] != 3 {
		t.Fatalf("after migrate-paths store_roots = %+v", sel)
	}
	if got := listIDs(t, home); len(got) != 3 {
		t.Fatalf("list after migration = %v", got)
	}
	// Dry run never touches the layout.
	home2, _, strayDB2 := strayXDGFixture(t)
	if stdout, stderr, code = runAgentDeck(t, home2, "migrate-paths", "--dry-run"); code == 0 || !strings.Contains(stdout, "conflict") {
		t.Fatalf("dry run on the stray layout: exit %d\n%s%s", code, stdout, stderr)
	}
	if _, err := os.Stat(strayDB2); err != nil {
		t.Fatalf("dry run moved the stray: %v", err)
	}
}

// TestCLI_Doctor_UnreadableStore: an unreadable store is reported as such,
// never as 0 sessions, and the stray beside it does not win.
func TestCLI_Doctor_UnreadableStore(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads mode 000 files")
	}
	home, legacyDB, _ := strayXDGFixture(t)
	legacyRoot := filepath.Dir(filepath.Dir(filepath.Dir(legacyDB)))
	if err := os.Chmod(legacyDB, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacyDB, 0o600) })

	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	for _, want := range []string{
		"Profile store: " + legacyRoot + " (legacy, reason=stray_xdg_store)",
		legacyRoot + ": unreadable (ch_support_test=unreadable) [active]",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "ch_support_test=-1") || strings.Contains(stdout, legacyRoot+": 0 sessions") {
		t.Fatalf("unreadable store rendered as a count:\n%s", stdout)
	}
	sel := doctorStoreRoots(t, home)
	if sel.Legacy.Unreadable != 1 || sel.Legacy.Profiles["ch_support_test"] != -1 {
		t.Fatalf("store_roots legacy = %+v", sel.Legacy)
	}
}
