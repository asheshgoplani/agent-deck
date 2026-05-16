package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// migrateTestSetup pins HOME to a temp dir and pre-creates the named profiles
// so MigrateSessionsToProfile can validate target existence. Returns the
// source + target *Storage and their *StateDB handles for direct seeding.
//
// Note: cross-profile migration explicitly forbids running while
// AGENTDECK_PROFILE is set to something stale (it forces the explicit-arg
// path), so we clear it.
func migrateTestSetup(t *testing.T, src, dst string) (srcStorage, dstStorage *Storage) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	var err error
	srcStorage, err = NewStorageWithProfile(src)
	if err != nil {
		t.Fatalf("open source profile: %v", err)
	}
	t.Cleanup(func() { _ = srcStorage.Close() })
	dstStorage, err = NewStorageWithProfile(dst)
	if err != nil {
		t.Fatalf("open target profile: %v", err)
	}
	t.Cleanup(func() { _ = dstStorage.Close() })
	return srcStorage, dstStorage
}

// makeRow returns an InstanceRow seeded with sentinel values across every
// migrated field, so PreservesAllFields can verify each one round-trips.
func makeRow(id, title, groupPath string) *statedb.InstanceRow {
	yolo := true
	tdBlob, _ := json.Marshal(map[string]any{
		"claude_session_id":  "claude-" + id,
		"claude_detected_at": 1700000000,
		"gemini_session_id":  "gem-" + id,
		"gemini_yolo_mode":   &yolo,
		"notes":              "migration sentinel " + id,
		"latest_prompt":      "hello",
		"loaded_mcp_names":   []string{"mcp-a", "mcp-b"},
		"channels":           []string{"chan-1"},
	})
	return &statedb.InstanceRow{
		ID:              id,
		Title:           title,
		ProjectPath:     "/tmp/migrate/" + id,
		GroupPath:       groupPath,
		Order:           7,
		Command:         "echo hello",
		Wrapper:         "claude",
		Tool:            "claude",
		Status:          "stopped",
		TmuxSession:     "tmux-" + id,
		TmuxSocketName:  "sock-" + id,
		CreatedAt:       time.Unix(1700000000, 0),
		LastAccessed:    time.Unix(1700001000, 0),
		ParentSessionID: "parent-of-" + id,
		IsConductor:     false,
		TitleLocked:     true,
		WorktreePath:    "/wt/" + id,
		WorktreeRepo:    "repo-" + id,
		WorktreeBranch:  "branch-" + id,
		ToolData:        tdBlob,
	}
}

func seedSession(t *testing.T, db *statedb.StateDB, row *statedb.InstanceRow) {
	t.Helper()
	if err := db.InsertInstanceRow(row); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
}

func TestMigrateSessionsToProfile_PreservesAllFields(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	row := makeRow("sess-1", "Project One", DefaultGroupPath)
	seedSession(t, src.GetDB(), row)

	result, err := MigrateSessionsToProfile("src", "dst", []string{"sess-1"}, ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(result.MovedSessionIDs) != 1 {
		t.Fatalf("expected 1 moved id, got %v", result.MovedSessionIDs)
	}

	// Source must be gone.
	srcRow, err := src.GetDB().LoadInstanceByID("sess-1")
	if err != nil {
		t.Fatalf("load src after: %v", err)
	}
	if srcRow != nil {
		t.Errorf("source still has session row %v", srcRow)
	}

	// Target must have a verbatim copy.
	got, err := dst.GetDB().LoadInstanceByID("sess-1")
	if err != nil {
		t.Fatalf("load dst after: %v", err)
	}
	if got == nil {
		t.Fatal("target missing migrated session")
	}
	for _, c := range []struct{ field, want, have string }{
		{"Title", row.Title, got.Title},
		{"ProjectPath", row.ProjectPath, got.ProjectPath},
		{"GroupPath", row.GroupPath, got.GroupPath},
		{"Tool", row.Tool, got.Tool},
		{"Status", row.Status, got.Status},
		{"TmuxSession", row.TmuxSession, got.TmuxSession},
		{"TmuxSocketName", row.TmuxSocketName, got.TmuxSocketName},
		{"ParentSessionID", row.ParentSessionID, got.ParentSessionID},
		{"WorktreePath", row.WorktreePath, got.WorktreePath},
		{"WorktreeRepo", row.WorktreeRepo, got.WorktreeRepo},
		{"WorktreeBranch", row.WorktreeBranch, got.WorktreeBranch},
	} {
		if c.have != c.want {
			t.Errorf("%s: want %q got %q", c.field, c.want, c.have)
		}
	}
	if got.Order != row.Order {
		t.Errorf("Order: want %d got %d", row.Order, got.Order)
	}
	if got.TitleLocked != row.TitleLocked {
		t.Errorf("TitleLocked: want %v got %v", row.TitleLocked, got.TitleLocked)
	}
	// tool_data must round-trip key-by-key. We don't assert byte equality
	// because INSERT OR REPLACE may re-serialize the JSON.
	var srcTD, dstTD map[string]any
	_ = json.Unmarshal(row.ToolData, &srcTD)
	_ = json.Unmarshal(got.ToolData, &dstTD)
	for k, v := range srcTD {
		if dvJSON, _ := json.Marshal(dstTD[k]); string(dvJSON) == "" {
			t.Errorf("tool_data missing key %q in dst", k)
			continue
		}
		svJSON, _ := json.Marshal(v)
		dvJSON, _ := json.Marshal(dstTD[k])
		if string(svJSON) != string(dvJSON) {
			t.Errorf("tool_data[%q]: want %s got %s", k, svJSON, dvJSON)
		}
	}
}

func TestMigrateSessionsToProfile_MigratesCostAndWatcherEvents(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	seedSession(t, src.GetDB(), makeRow("sess-cost", "S", DefaultGroupPath))

	// Two cost events.
	for _, ce := range []*statedb.CostEventRow{
		{ID: "c1", SessionID: "sess-cost", Timestamp: time.Now().UTC().Format(time.RFC3339), Model: "claude-sonnet-4-6", CostMicrodollars: 1234},
		{ID: "c2", SessionID: "sess-cost", Timestamp: time.Now().UTC().Format(time.RFC3339), Model: "gemini-2.5", CostMicrodollars: 5678},
	} {
		if err := src.GetDB().InsertCostEventRow(ce); err != nil {
			t.Fatalf("seed cost: %v", err)
		}
	}

	// Watcher + watcher events (we need a watcher row first because of FK).
	if err := src.GetDB().SaveWatcher(&statedb.WatcherRow{
		ID: "wid-1", Name: "watch-a", Type: "github", ConfigPath: "/tmp/w.toml",
		Status: "running", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed watcher: %v", err)
	}
	if err := dst.GetDB().SaveWatcher(&statedb.WatcherRow{
		ID: "wid-1", Name: "watch-a", Type: "github", ConfigPath: "/tmp/w.toml",
		Status: "running", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed watcher dst: %v", err)
	}
	for _, we := range []*statedb.WatcherEventRow{
		{WatcherID: "wid-1", DedupKey: "d1", Sender: "user", Subject: "hi", SessionID: "sess-cost", CreatedAt: time.Now()},
		{WatcherID: "wid-1", DedupKey: "d2", Sender: "user", Subject: "yo", TriageSessionID: "sess-cost", CreatedAt: time.Now()},
	} {
		if err := src.GetDB().InsertWatcherEventRow(we); err != nil {
			t.Fatalf("seed watcher event: %v", err)
		}
	}

	result, err := MigrateSessionsToProfile("src", "dst", []string{"sess-cost"}, ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.MovedCostEvents != 2 {
		t.Errorf("expected 2 cost events moved, got %d", result.MovedCostEvents)
	}
	if result.MovedWatcherEvents != 2 {
		t.Errorf("expected 2 watcher events moved, got %d", result.MovedWatcherEvents)
	}

	// Verify dst has the cost rows; src has none.
	dstCosts, _ := dst.GetDB().LoadCostEventsForSession("sess-cost")
	if len(dstCosts) != 2 {
		t.Errorf("dst cost rows: want 2, got %d", len(dstCosts))
	}
	srcCosts, _ := src.GetDB().LoadCostEventsForSession("sess-cost")
	if len(srcCosts) != 0 {
		t.Errorf("src cost rows should be empty, got %d", len(srcCosts))
	}
	dstWE, _ := dst.GetDB().LoadWatcherEventsForSession("sess-cost")
	if len(dstWE) != 2 {
		t.Errorf("dst watcher events: want 2, got %d", len(dstWE))
	}
	srcWE, _ := src.GetDB().LoadWatcherEventsForSession("sess-cost")
	if len(srcWE) != 0 {
		t.Errorf("src watcher events should be empty, got %d", len(srcWE))
	}
}

func TestMigrateSessionsToProfile_CreatesMissingGroup(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")

	// Source group with metadata; target lacks it.
	groupPath := "work/api"
	if err := src.GetDB().SaveGroup(&statedb.GroupRow{
		Path: groupPath, Name: "api", Expanded: true, Order: 5, DefaultPath: "/var/api",
	}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	row := makeRow("sess-g", "Grouped", groupPath)
	seedSession(t, src.GetDB(), row)

	result, err := MigrateSessionsToProfile("src", "dst", []string{"sess-g"}, ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(result.CreatedGroups) != 1 || result.CreatedGroups[0] != groupPath {
		t.Errorf("expected CreatedGroups=[%q], got %v", groupPath, result.CreatedGroups)
	}
	got, _ := dst.GetDB().LoadGroup(groupPath)
	if got == nil {
		t.Fatal("target missing migrated group")
	}
	if got.DefaultPath != "/var/api" {
		t.Errorf("DefaultPath: want /var/api got %q", got.DefaultPath)
	}
	if got.Order != 5 {
		t.Errorf("Order: want 5 got %d", got.Order)
	}
}

func TestMigrateSessionsToProfile_RefusesRunning(t *testing.T) {
	src, _ := migrateTestSetup(t, "src", "dst")
	row := makeRow("sess-run", "Running", DefaultGroupPath)
	row.Status = "running"
	seedSession(t, src.GetDB(), row)

	_, err := MigrateSessionsToProfile("src", "dst", []string{"sess-run"}, ProfileMigrateOptions{})
	if !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("want ErrSessionRunning, got %v", err)
	}

	// Source row must still exist (no partial state).
	got, _ := src.GetDB().LoadInstanceByID("sess-run")
	if got == nil {
		t.Fatal("source row was incorrectly deleted on running-refusal")
	}
}

func TestMigrateSessionsToProfile_ForceAllowsRunning(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	row := makeRow("sess-run-force", "Running", DefaultGroupPath)
	row.Status = "running"
	seedSession(t, src.GetDB(), row)

	if _, err := MigrateSessionsToProfile("src", "dst", []string{"sess-run-force"}, ProfileMigrateOptions{Force: true}); err != nil {
		t.Fatalf("migrate --force: %v", err)
	}
	got, _ := dst.GetDB().LoadInstanceByID("sess-run-force")
	if got == nil {
		t.Fatal("target missing forced-migrated session")
	}
	if got.Status != "running" {
		t.Errorf("Status should round-trip even when forcing; got %q", got.Status)
	}
	srcRow, _ := src.GetDB().LoadInstanceByID("sess-run-force")
	if srcRow != nil {
		t.Error("source still has row after force migration")
	}
}

func TestMigrateSessionsToProfile_RefusesMissingTargetProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	// Create source only.
	src, err := NewStorageWithProfile("src")
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer src.Close()

	_, err = MigrateSessionsToProfile("src", "phantom", []string{"any"}, ProfileMigrateOptions{})
	if !errors.Is(err, ErrProfileMissing) {
		t.Fatalf("want ErrProfileMissing, got %v", err)
	}
}

func TestMigrateSessionsToProfile_RefusesSameProfile(t *testing.T) {
	migrateTestSetup(t, "src", "dst")
	_, err := MigrateSessionsToProfile("same", "same", []string{"x"}, ProfileMigrateOptions{})
	if !errors.Is(err, ErrSameProfile) {
		t.Fatalf("want ErrSameProfile, got %v", err)
	}
}

func TestMigrateSessionsToProfile_Idempotent(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	row := makeRow("sess-idem", "Idem", DefaultGroupPath)
	seedSession(t, src.GetDB(), row)

	if _, err := MigrateSessionsToProfile("src", "dst", []string{"sess-idem"}, ProfileMigrateOptions{}); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Re-run: source is empty, target has the row — must succeed as a no-op.
	result, err := MigrateSessionsToProfile("src", "dst", []string{"sess-idem"}, ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("second migrate (idempotent): %v", err)
	}
	if len(result.SkippedIdempotent) != 1 {
		t.Errorf("expected SkippedIdempotent=[sess-idem], got %v", result.SkippedIdempotent)
	}
	got, _ := dst.GetDB().LoadInstanceByID("sess-idem")
	if got == nil {
		t.Fatal("dst row vanished on idempotent re-run")
	}
}

func TestMigrateConductorToProfile_MovesChildren(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")

	// Conductor + 2 workers under it.
	conductor := makeRow("cond-1", ConductorSessionTitle("alpha"), DefaultGroupPath)
	conductor.IsConductor = true
	conductor.ParentSessionID = ""
	seedSession(t, src.GetDB(), conductor)
	for _, id := range []string{"worker-a", "worker-b"} {
		w := makeRow(id, "worker-"+id, DefaultGroupPath)
		w.ParentSessionID = "cond-1"
		seedSession(t, src.GetDB(), w)
	}

	// Pre-create meta.json so the helper has something to rewrite.
	if err := SaveConductorMeta(&ConductorMeta{
		Name:    "alpha",
		Agent:   ConductorAgentClaude,
		Profile: "src",
	}); err != nil {
		t.Fatalf("seed conductor meta: %v", err)
	}

	result, err := MigrateConductorToProfile("alpha", "src", "dst", ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("migrate conductor: %v", err)
	}
	if len(result.MovedSessionIDs) != 3 {
		t.Errorf("expected 3 sessions moved (conductor+2 workers), got %d (%v)", len(result.MovedSessionIDs), result.MovedSessionIDs)
	}
	if !result.MetaUpdated {
		t.Error("MetaUpdated should be true")
	}

	// All three should be at dst, none at src.
	for _, id := range []string{"cond-1", "worker-a", "worker-b"} {
		if r, _ := dst.GetDB().LoadInstanceByID(id); r == nil {
			t.Errorf("dst missing migrated id %q", id)
		}
		if r, _ := src.GetDB().LoadInstanceByID(id); r != nil {
			t.Errorf("src still has migrated id %q", id)
		}
	}

	// Verify meta.json was rewritten.
	got, err := LoadConductorMeta("alpha")
	if err != nil {
		t.Fatalf("reload meta: %v", err)
	}
	if got.Profile != "dst" {
		t.Errorf("meta.json profile: want dst got %q", got.Profile)
	}
}

func TestMigrateConductorToProfile_AtomicMetaWriteNoTempLeftover(t *testing.T) {
	src, _ := migrateTestSetup(t, "src", "dst")

	cond := makeRow("c2", ConductorSessionTitle("beta"), DefaultGroupPath)
	cond.IsConductor = true
	seedSession(t, src.GetDB(), cond)
	if err := SaveConductorMeta(&ConductorMeta{
		Name: "beta", Agent: ConductorAgentClaude, Profile: "src",
	}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if _, err := MigrateConductorToProfile("beta", "src", "dst", ProfileMigrateOptions{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dir, _ := ConductorNameDir("beta")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "meta.json.tmp.") {
			t.Errorf("atomic write left a temp file: %s", e.Name())
		}
	}
}

func TestMigrateGroupToProfile_BatchMovesEverything(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")

	groupPath := "work/api"
	if err := src.GetDB().SaveGroup(&statedb.GroupRow{
		Path: groupPath, Name: "api", Expanded: true, Order: 3,
	}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	for _, id := range []string{"g-1", "g-2", "g-3"} {
		seedSession(t, src.GetDB(), makeRow(id, id, groupPath))
	}
	// Plus a session in a different group — must NOT migrate.
	otherRow := makeRow("other-1", "other", "elsewhere")
	if err := src.GetDB().SaveGroup(&statedb.GroupRow{Path: "elsewhere", Name: "elsewhere", Expanded: true}); err != nil {
		t.Fatalf("seed other group: %v", err)
	}
	seedSession(t, src.GetDB(), otherRow)

	result, err := MigrateGroupToProfile(groupPath, "src", "dst", ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("migrate group: %v", err)
	}
	if len(result.MovedSessionIDs) != 3 {
		t.Errorf("expected 3 sessions moved, got %d", len(result.MovedSessionIDs))
	}
	for _, id := range []string{"g-1", "g-2", "g-3"} {
		if r, _ := dst.GetDB().LoadInstanceByID(id); r == nil {
			t.Errorf("dst missing %q", id)
		}
		if r, _ := src.GetDB().LoadInstanceByID(id); r != nil {
			t.Errorf("src still has %q", id)
		}
	}
	// Untouched session.
	if r, _ := src.GetDB().LoadInstanceByID("other-1"); r == nil {
		t.Error("group-scoped migration should not touch other-1")
	}
	if r, _ := dst.GetDB().LoadInstanceByID("other-1"); r != nil {
		t.Error("group-scoped migration leaked other-1 to dst")
	}
}

// TestMigrateSessionsToProfile_ConcurrentDifferentSessions exercises
// withBusyRetry: two goroutines migrate different sessions into the same dst
// at the same time. Both must succeed.
func TestMigrateSessionsToProfile_ConcurrentDifferentSessions(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	for _, id := range []string{"par-1", "par-2"} {
		seedSession(t, src.GetDB(), makeRow(id, id, DefaultGroupPath))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"par-1", "par-2"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := MigrateSessionsToProfile("src", "dst", []string{id}, ProfileMigrateOptions{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent migrate failed: %v", err)
		}
	}
	for _, id := range []string{"par-1", "par-2"} {
		if r, _ := dst.GetDB().LoadInstanceByID(id); r == nil {
			t.Errorf("dst missing %q after concurrent migrate", id)
		}
	}
}

// TestMigrateConductorToProfile_RescuesStrandedChildrenOnRerun simulates the
// partial-failure case Copilot flagged: a prior run moved the conductor and
// the first worker, then crashed before the second worker migrated. Re-running
// must sweep the stranded worker rather than just updating meta.json.
func TestMigrateConductorToProfile_RescuesStrandedChildrenOnRerun(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")

	// Conductor + worker A already in dst (the prior-run survivors).
	cond := makeRow("c-resume", ConductorSessionTitle("rescue"), DefaultGroupPath)
	cond.IsConductor = true
	seedSession(t, dst.GetDB(), cond)
	wA := makeRow("worker-a-resume", "worker-a", DefaultGroupPath)
	wA.ParentSessionID = cond.ID
	seedSession(t, dst.GetDB(), wA)

	// Worker B is stranded in src, still pointing at the (now-dst) conductor.
	wB := makeRow("worker-b-resume", "worker-b", DefaultGroupPath)
	wB.ParentSessionID = cond.ID
	seedSession(t, src.GetDB(), wB)

	if err := SaveConductorMeta(&ConductorMeta{
		Name: "rescue", Agent: ConductorAgentClaude, Profile: "src",
	}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	result, err := MigrateConductorToProfile("rescue", "src", "dst", ProfileMigrateOptions{})
	if err != nil {
		t.Fatalf("rerun migrate: %v", err)
	}
	// The stranded worker must be at dst now and gone from src.
	if r, _ := dst.GetDB().LoadInstanceByID(wB.ID); r == nil {
		t.Errorf("stranded worker %q was not rescued to dst", wB.ID)
	}
	if r, _ := src.GetDB().LoadInstanceByID(wB.ID); r != nil {
		t.Errorf("stranded worker %q still in src after re-run", wB.ID)
	}
	// Conductor is reported as idempotent-skipped.
	foundCond := false
	for _, id := range result.SkippedIdempotent {
		if id == cond.ID {
			foundCond = true
		}
	}
	if !foundCond {
		t.Errorf("expected conductor %q in SkippedIdempotent, got %v", cond.ID, result.SkippedIdempotent)
	}
	got, _ := LoadConductorMeta("rescue")
	if got == nil || got.Profile != "dst" {
		t.Errorf("meta.json not updated to dst on rescue rerun: %+v", got)
	}
}

// TestMigrateSessionsToProfile_CopiesReferencedWatcherRow verifies fix #3 from
// the Copilot review: watcher_events.watcher_id is FK-constrained, so we
// must copy the watchers row from src→dst before inserting events.
func TestMigrateSessionsToProfile_CopiesReferencedWatcherRow(t *testing.T) {
	src, dst := migrateTestSetup(t, "src", "dst")
	seedSession(t, src.GetDB(), makeRow("sess-fk", "S", DefaultGroupPath))

	// Watcher exists in src only. Without the fix, the watcher_event insert
	// at dst would fail with FOREIGN KEY constraint failed.
	if err := src.GetDB().SaveWatcher(&statedb.WatcherRow{
		ID: "w-fk", Name: "fk-watch", Type: "github", ConfigPath: "/tmp/w.toml",
		Status: "stopped", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed src watcher: %v", err)
	}
	if err := src.GetDB().InsertWatcherEventRow(&statedb.WatcherEventRow{
		WatcherID: "w-fk", DedupKey: "d-fk", SessionID: "sess-fk", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed watcher event: %v", err)
	}

	if _, err := MigrateSessionsToProfile("src", "dst", []string{"sess-fk"}, ProfileMigrateOptions{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// dst must have the watcher row (copied) AND the event.
	w, _ := dst.GetDB().LoadWatcherByID("w-fk")
	if w == nil {
		t.Error("dst missing watcher row that was referenced by a migrated event")
	}
	events, _ := dst.GetDB().LoadWatcherEventsForSession("sess-fk")
	if len(events) != 1 {
		t.Errorf("dst missing migrated watcher event: %d", len(events))
	}
}

// TestMigrateGroupToProfile_EmptyGroupIsNoOp verifies fix #5: re-running a
// group migration after the source group has been emptied must succeed.
func TestMigrateGroupToProfile_EmptyGroupIsNoOp(t *testing.T) {
	migrateTestSetup(t, "src", "dst")
	// No sessions seeded in src.
	result, err := MigrateGroupToProfile("nonexistent-group", "src", "dst", ProfileMigrateOptions{})
	if err != nil {
		t.Errorf("expected empty group to be a no-op, got error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result on no-op")
	}
	if len(result.MovedSessionIDs) != 0 {
		t.Errorf("expected zero moved sessions, got %v", result.MovedSessionIDs)
	}
}

// TestRollbackTargetWrites_PreservesPreexistingRows ensures fix #4: if the
// migration is rerun and the destination already has the session row + its
// associated cost/watcher rows from a prior partial run, a source-delete
// failure must NOT bulk-delete those rows at the destination.
func TestRollbackTargetWrites_PreservesPreexistingRows(t *testing.T) {
	_, dst := migrateTestSetup(t, "src", "dst")

	// Seed dst with a session + cost event that we'd consider "legitimately
	// pre-existing" from a prior partial migration.
	seedSession(t, dst.GetDB(), makeRow("preexist", "P", DefaultGroupPath))
	if err := dst.GetDB().InsertCostEventRow(&statedb.CostEventRow{
		ID: "cp-1", SessionID: "preexist", Timestamp: time.Now().UTC().Format(time.RFC3339), Model: "m", CostMicrodollars: 100,
	}); err != nil {
		t.Fatalf("seed dst cost: %v", err)
	}

	// Simulate rollback with targetAlreadyHadInstance=true: must NOT delete
	// the pre-existing cost row.
	rollbackTargetWrites(dst.GetDB(), "preexist", true)

	costs, _ := dst.GetDB().LoadCostEventsForSession("preexist")
	if len(costs) != 1 {
		t.Errorf("rollback with targetAlreadyHadInstance=true clobbered pre-existing cost rows: have %d, want 1", len(costs))
	}
	inst, _ := dst.GetDB().LoadInstanceByID("preexist")
	if inst == nil {
		t.Error("rollback clobbered pre-existing instance row")
	}
}

// Sanity check: the helper that pre-validates target existence rejects a
// directory-without-state.db case.
func TestRequireProfileExists_RejectsBareDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create a profile directory but no state.db inside.
	dir, _ := GetProfileDir("phantom")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := requireProfileExists("phantom"); !errors.Is(err, ErrProfileMissing) {
		t.Errorf("want ErrProfileMissing, got %v", err)
	}
	// Sanity: a real state.db is accepted.
	if _, err := os.Create(filepath.Join(dir, "state.db")); err != nil {
		t.Fatalf("touch state.db: %v", err)
	}
	if err := requireProfileExists("phantom"); err != nil {
		t.Errorf("want nil, got %v", err)
	}
}
