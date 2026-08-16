// Stress tests for custom-tool generic_session_id SQLite persistence:
// sticky merge, concurrent writers, intentional clear, legacy rows, and
// independence from claude_session_id. See design note
// docs/design/2026-08-11-persist-custom-tool-session-id.md.
package session

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestStress_StaleFullSavePreservesStickyGenericID is the primary durability
// invariant: WriteGenericSessionBinding (or a prior save with id) lands the
// id, then a full SaveWithGroups from an Instance snapshot whose
// GenericSessionID is still empty must NOT wipe it (sticky / extras merge).
func TestStress_StaleFullSavePreservesStickyGenericID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("sticky-stale", "/tmp/proj")
	inst.Tool = "shell"
	inst.Color = "#ff00aa"
	// First save: no generic id yet.
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Live capture path: targeted write while in-memory snapshot stays empty.
	detected := time.Unix(1_700_000_200, 0).UTC()
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "live-captured-id", inst.Tool, inst.Command, LocationOf(inst).String(), detected); err != nil {
		t.Fatalf("WriteGenericSessionBinding: %v", err)
	}

	// Stale full-table save: Instance still has empty GenericSessionID.
	stale := NewInstance("sticky-stale", "/tmp/proj")
	stale.ID = inst.ID
	stale.Tool = "shell"
	stale.Color = "#00ff00" // unrelated field change
	stale.GenericSessionID = ""
	stale.GenericDetectedAt = time.Time{}
	if err := storage.SaveWithGroups([]*Instance{stale}, NewGroupTreeWithGroups([]*Instance{stale}, nil)); err != nil {
		t.Fatalf("stale SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len=%d", len(loaded))
	}
	if got := loaded[0].GenericSessionID; got != "live-captured-id" {
		t.Fatalf("sticky failed: GenericSessionID=%q want live-captured-id (stale full save wiped binding)", got)
	}
	if loaded[0].Color != "#00ff00" {
		t.Fatalf("color not updated: %q", loaded[0].Color)
	}
}

// TestStress_ExplicitClearViaBindingThenLoad ensures json_remove clear is
// durable and a subsequent load returns empty (not sticky-restored).
func TestStress_ExplicitClearViaBindingThenLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("clear-bind", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = "to-be-cleared"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", inst.Tool, inst.Command, LocationOf(inst).String(), time.Time{}); err != nil {
		t.Fatalf("clear binding: %v", err)
	}

	// Stale save after clear must not resurrect: DB keys are gone, so sticky
	// has nothing to preserve. New Instance (do not copy *inst — it holds a mutex).
	stale := NewInstance(inst.Title, inst.ProjectPath)
	stale.ID = inst.ID
	stale.Tool = inst.Tool
	if err := storage.SaveWithGroups([]*Instance{stale}, NewGroupTreeWithGroups([]*Instance{stale}, nil)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("after clear+stale-save got %q", loaded[0].GenericSessionID)
	}
	if !loaded[0].GenericDetectedAt.IsZero() {
		t.Fatalf("detected_at should be zero after clear, got %v", loaded[0].GenericDetectedAt)
	}
}

// TestStress_SetFieldClearThenSave_StickyHole guards intentional clear via
// SetField + SaveWithGroups with GetGlobal nil (CLI shape). SetField must set
// genericSessionIDCleared so instanceToRow emits explicit empty; sticky then
// honors the clear without needing WriteGenericSessionBinding.
func TestStress_SetFieldClearThenSave_StickyHole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("setfield-clear", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = "must-clear"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	// Simulate CLI: SetField then SaveWithGroups. GetGlobal is nil here (same
	// as pure `agent-deck session set` without TUI SetGlobal).
	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	if _, _, err := SetField(inst, FieldToolSessionID, "", nil); err != nil {
		t.Fatal(err)
	}
	if inst.GenericSessionID != "" {
		t.Fatalf("in-memory clear failed: %q", inst.GenericSessionID)
	}
	if !inst.genericSessionIDCleared {
		t.Fatal("SetField clear must set genericSessionIDCleared")
	}

	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("SetField clear + Save left id %q — intentionalClear flag not applied on save", loaded[0].GenericSessionID)
	}
}

// TestStress_RawEmptySaveWithoutClearFlag_PreservesSticky documents that
// zeroing GenericSessionID in memory without the intentional-clear flag is
// treated as an unaware writer: sticky re-hydrates the prior id. Operators
// must clear via SetField / clearSessionBindingForFreshStart / WriteGenericSessionBinding.
func TestStress_RawEmptySaveWithoutClearFlag_PreservesSticky(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("sticky-hole-demo", "/tmp")
	inst.Tool = "shell"
	inst.GenericSessionID = "zombie-id"
	inst.GenericDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	inst.GenericSessionID = ""
	inst.GenericDetectedAt = time.Time{}
	// genericSessionIDCleared left false — unaware empty.
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "zombie-id" {
		t.Fatalf("expected sticky preserve of zombie-id when save omits key, got %q", loaded[0].GenericSessionID)
	}
}

// TestStress_WriteBindingDoesNotClobberOtherToolDataKeys proves json_set /
// json_remove only touch generic_* keys.
func TestStress_WriteBindingDoesNotClobberOtherToolDataKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("no-clobber", "/tmp")
	inst.Tool = "claude"
	inst.ClaudeSessionID = "claude-uuid-keep"
	inst.ClaudeDetectedAt = time.Unix(1_700_000_000, 0).UTC()
	inst.Color = "#abcdef"
	inst.LastStartedAt = time.Unix(1_700_000_050, 0).UTC()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	if err := storage.db.WriteGenericSessionBinding(inst.ID, "generic-1", inst.Tool, inst.Command, LocationOf(inst).String(), time.Unix(1_700_000_100, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	// Read raw tool_data and assert siblings remain.
	var raw sql.NullString
	if err := storage.db.DB().QueryRow(`SELECT tool_data FROM instances WHERE id = ?`, inst.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"claude_session_id":  `"claude-uuid-keep"`,
		"generic_session_id": `"generic-1"`,
		"color":              `"#abcdef"`,
	} {
		if string(m[k]) != want {
			t.Errorf("%s = %s, want %s (full blob: %s)", k, m[k], want, raw.String)
		}
	}
	if _, ok := m["last_started_at"]; !ok {
		t.Errorf("last_started_at clobbered; blob=%s", raw.String)
	}

	// Clear generic only — siblings must remain.
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "", inst.Tool, inst.Command, LocationOf(inst).String(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.DB().QueryRow(`SELECT tool_data FROM instances WHERE id = ?`, inst.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	m = map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(raw.String), &m)
	if _, ok := m["generic_session_id"]; ok {
		t.Fatalf("generic_session_id still present after clear: %s", raw.String)
	}
	if string(m["claude_session_id"]) != `"claude-uuid-keep"` {
		t.Fatalf("clear generic clobbered claude_session_id: %s", raw.String)
	}
	if string(m["color"]) != `"#abcdef"` {
		t.Fatalf("clear generic clobbered color: %s", raw.String)
	}
}

// TestStress_DetectedAtZeroVsNonZero covers WriteGenericSessionBinding's
// zero-time fallback (uses now) vs explicit stamp, and tool_data write omit.
func TestStress_DetectedAtZeroVsNonZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("detected-at", "/tmp")
	inst.Tool = "shell"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	before := time.Now().Add(-2 * time.Second)
	if err := storage.db.WriteGenericSessionBinding(inst.ID, "id-zero-at", inst.Tool, inst.Command, LocationOf(inst).String(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(2 * time.Second)

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].GenericSessionID != "id-zero-at" {
		t.Fatalf("id=%q", loaded[0].GenericSessionID)
	}
	at := loaded[0].GenericDetectedAt
	if at.IsZero() {
		t.Fatal("zero detectedAt on WriteGenericSessionBinding must stamp now, not leave zero")
	}
	if at.Before(before) || at.After(after) {
		t.Fatalf("detected_at %v outside [%v,%v]", at, before, after)
	}

	// Explicit non-zero stamp round-trips via full Save path.
	want := time.Unix(1_700_000_777, 0).UTC()
	inst.GenericSessionID = "id-explicit-at"
	inst.GenericDetectedAt = want
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded2[0].GenericDetectedAt.Equal(want) {
		t.Fatalf("detected_at=%v want %v", loaded2[0].GenericDetectedAt, want)
	}

	// WriteGenericSessionIDToToolData with zero detectedAt omits the key.
	td := WriteGenericSessionIDToToolData(nil, "only-id", time.Time{}, false)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["generic_session_id"]) != `"only-id"` {
		t.Fatalf("id missing: %s", td)
	}
	if _, ok := m["generic_detected_at"]; ok {
		t.Fatalf("zero detectedAt should omit key, got %s", td)
	}
}

// TestStress_RapidAlternateWrites_LastWriteWins hammers two ids concurrently
// via WriteGenericSessionBinding; final load must be one of the two ids
// (last committed writer wins under SQLite serialization).
func TestStress_RapidAlternateWrites_LastWriteWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("race-ids", "/tmp")
	inst.Tool = "shell"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := storage.db.WriteGenericSessionBinding(inst.ID, "id-A", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := storage.db.WriteGenericSessionBinding(inst.ID, "id-B", inst.Tool, inst.Command, LocationOf(inst).String(), time.Now()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	got := loaded[0].GenericSessionID
	if got != "id-A" && got != "id-B" {
		t.Fatalf("final id %q not in {id-A,id-B}", got)
	}
}

// TestStress_LegacyRowWithoutGenericKey_NoCrash loads a pre-feature tool_data
// blob that never had generic_session_id.
func TestStress_LegacyRowWithoutGenericKey_NoCrash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Pre-feature shape: color + claude only.
	row := &statedb.InstanceRow{
		ID: "legacy-1", Title: "legacy", Status: "idle", Tool: "shell",
		CreatedAt: now, LastAccessed: now, GroupPath: "default",
		ToolData: json.RawMessage(`{"color":"#111","claude_session_id":"only-claude"}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}

	storage := &Storage{db: db, dbPath: dbPath, profile: "_legacy"}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len=%d", len(loaded))
	}
	if loaded[0].GenericSessionID != "" {
		t.Fatalf("legacy generic=%q", loaded[0].GenericSessionID)
	}
	if !loaded[0].GenericDetectedAt.IsZero() {
		t.Fatalf("legacy detected_at=%v", loaded[0].GenericDetectedAt)
	}
	// Readers must not panic on empty/malformed either.
	if got := ReadGenericSessionIDFromToolData(nil); got != "" {
		t.Fatalf("nil blob: %q", got)
	}
	if got := ReadGenericSessionIDFromToolData(json.RawMessage(`not-json`)); got != "" {
		t.Fatalf("malformed: %q", got)
	}
	if got := ReadGenericSessionIDFromToolData(json.RawMessage(`{}`)); got != "" {
		t.Fatalf("empty object: %q", got)
	}
}

// TestStress_ClaudeAndGenericIndependent ensures both ids coexist and
// operations on one do not affect the other.
func TestStress_ClaudeAndGenericIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("both-ids", "/tmp")
	inst.Tool = "claude"
	inst.ClaudeSessionID = "claude-AAA"
	inst.ClaudeDetectedAt = time.Unix(100, 0).UTC()
	inst.GenericSessionID = "generic-BBB"
	inst.GenericDetectedAt = time.Unix(200, 0).UTC()
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	if err := storage.db.WriteGenericSessionBinding(inst.ID, "generic-CCC", inst.Tool, inst.Command, LocationOf(inst).String(), time.Unix(300, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].ClaudeSessionID != "claude-AAA" {
		t.Fatalf("claude id mutated: %q", loaded[0].ClaudeSessionID)
	}
	if loaded[0].GenericSessionID != "generic-CCC" {
		t.Fatalf("generic id=%q", loaded[0].GenericSessionID)
	}

	if err := storage.db.WriteClaudeSessionBinding(inst.ID, "claude-DDD", time.Unix(400, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if loaded2[0].ClaudeSessionID != "claude-DDD" {
		t.Fatalf("claude=%q", loaded2[0].ClaudeSessionID)
	}
	if loaded2[0].GenericSessionID != "generic-CCC" {
		t.Fatalf("generic clobbered by claude write: %q", loaded2[0].GenericSessionID)
	}
}

// TestStress_ProfileIsolation_SeparateStateDBs confirms default vs named
// profile paths hold independent generic_session_id values.
func TestStress_ProfileIsolation_SeparateStateDBs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Two independent Storage handles (as NewStorageWithProfile would for
	// different profiles) — we open distinct state.db files directly to avoid
	// profile auto-create policy noise.
	open := func(name string) *Storage {
		t.Helper()
		dir := filepath.Join(home, "profiles", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(dir, "state.db")
		db, err := statedb.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &Storage{db: db, dbPath: dbPath, profile: name}
	}

	def := open("default")
	work := open("work")

	iDef := NewInstance("shared-title", "/tmp")
	iDef.Tool = "shell"
	iDef.GenericSessionID = "default-sid"
	if err := def.SaveWithGroups([]*Instance{iDef}, NewGroupTreeWithGroups([]*Instance{iDef}, nil)); err != nil {
		t.Fatal(err)
	}

	iWork := NewInstance("shared-title", "/tmp")
	iWork.Tool = "shell"
	iWork.GenericSessionID = "work-sid"
	// Force same instance ID to prove DBs are isolated even on id collision.
	iWork.ID = iDef.ID
	if err := work.SaveWithGroups([]*Instance{iWork}, NewGroupTreeWithGroups([]*Instance{iWork}, nil)); err != nil {
		t.Fatal(err)
	}

	ld, _, err := def.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	lw, _, err := work.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if ld[0].GenericSessionID != "default-sid" {
		t.Fatalf("default profile: %q", ld[0].GenericSessionID)
	}
	if lw[0].GenericSessionID != "work-sid" {
		t.Fatalf("work profile: %q", lw[0].GenericSessionID)
	}
	if def.Path() == work.Path() {
		t.Fatal("profile state.db paths must differ")
	}
}
