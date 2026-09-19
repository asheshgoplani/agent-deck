package statedb

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// SchemaVersion must not move for the recall tables: they are created with
// CREATE TABLE IF NOT EXISTS, and an older binary sharing the same state.db
// (remote hosts, other profiles) would rewrite a bumped version back down on
// every open because Migrate has no downgrade guard.
func TestRecallTables_NoSchemaVersionBump(t *testing.T) {
	db := openTestDB(t)
	var version string
	if err := db.DB().QueryRow(`SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != strconv.Itoa(SchemaVersion) || SchemaVersion != 13 {
		t.Fatalf("schema_version = %s, SchemaVersion = %d; want 13 (no bump for recall tables)", version, SchemaVersion)
	}
	for _, table := range []string{"session_hints", "session_tags", "session_links"} {
		var name string
		if err := db.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing after Migrate: %v", table, err)
		}
	}
	// Migrate is idempotent: a second run over an existing database is a no-op.
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestSessionHint_UpsertReplacesRatherThanDuplicates(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", nil)

	if err := db.SetSessionHint(HintScopeInstance, "i1", "ticket", "SB-412", HintSourceCLICreate, ""); err != nil {
		t.Fatalf("SetSessionHint: %v", err)
	}
	if err := db.SetSessionHint(HintScopeInstance, "i1", "ticket", "SB-413", HintSourceAnnotate, "me"); err != nil {
		t.Fatalf("SetSessionHint (correction): %v", err)
	}
	hints, err := db.ListSessionHints(HintScopeInstance, "i1")
	if err != nil {
		t.Fatalf("ListSessionHints: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("hints = %+v; want exactly one ticket row (UPSERT replaces)", hints)
	}
	h := hints[0]
	if h.Key != "ticket" || h.Value != "SB-413" || h.Source != HintSourceAnnotate || h.Author != "me" {
		t.Fatalf("hint = %+v; want ticket=SB-413 from annotate by me", h)
	}
	if h.Seq != 1 {
		t.Fatalf("seq = %d; want 1 after one replacement", h.Seq)
	}
	var rows int
	if err := db.DB().QueryRow(`SELECT count(*) FROM session_hints WHERE scope_id='i1'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("physical rows = %d, err=%v; want 1", rows, err)
	}
}

func TestSessionHint_RejectsBadInput(t *testing.T) {
	db := openTestDB(t)
	cases := []struct{ scopeKind, scopeID, key, value string }{
		{"", "i1", "ticket", "x"},
		{"bogus", "i1", "ticket", "x"},
		{HintScopeInstance, "", "ticket", "x"},
		{HintScopeInstance, "i1", "", "x"},
		{HintScopeInstance, "i1", "bad key", "x"},
		{HintScopeInstance, "i1", "ticket", ""},
	}
	for _, c := range cases {
		if err := db.SetSessionHint(c.scopeKind, c.scopeID, c.key, c.value, HintSourceAnnotate, ""); err == nil {
			t.Errorf("SetSessionHint(%q,%q,%q,%q) accepted; want error", c.scopeKind, c.scopeID, c.key, c.value)
		}
	}
}

func TestSessionHint_Unset(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", nil)
	for _, kv := range [][2]string{{"why", "3rd regression"}, {"purpose", "fix auth"}} {
		if err := db.SetSessionHint(HintScopeInstance, "i1", kv[0], kv[1], HintSourceCLICreate, ""); err != nil {
			t.Fatalf("SetSessionHint: %v", err)
		}
	}
	removed, err := db.UnsetSessionHint(HintScopeInstance, "i1", "why")
	if err != nil || !removed {
		t.Fatalf("UnsetSessionHint = %v, %v; want true, nil", removed, err)
	}
	removed, err = db.UnsetSessionHint(HintScopeInstance, "i1", "why")
	if err != nil || removed {
		t.Fatalf("second UnsetSessionHint = %v, %v; want false, nil", removed, err)
	}
	hints, _ := db.ListSessionHints(HintScopeInstance, "i1")
	if len(hints) != 1 || hints[0].Key != "purpose" {
		t.Fatalf("hints after unset = %+v; want only purpose", hints)
	}
}

func TestSessionTags_AddRemoveReadd(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", nil)
	for _, tag := range []string{"auth", "flaky", "auth"} {
		if err := db.AddSessionTag(HintScopeInstance, "i1", tag, HintSourceCLICreate); err != nil {
			t.Fatalf("AddSessionTag(%q): %v", tag, err)
		}
	}
	tags, err := db.ListSessionTags(HintScopeInstance, "i1")
	if err != nil {
		t.Fatalf("ListSessionTags: %v", err)
	}
	if len(tags) != 2 || tags[0].Tag != "auth" || tags[1].Tag != "flaky" {
		t.Fatalf("tags = %+v; want [auth flaky] in insertion order", tags)
	}
	removed, err := db.RemoveSessionTag(HintScopeInstance, "i1", "flaky")
	if err != nil || !removed {
		t.Fatalf("RemoveSessionTag = %v, %v; want true, nil", removed, err)
	}
	if removed, _ := db.RemoveSessionTag(HintScopeInstance, "i1", "flaky"); removed {
		t.Fatal("removing an already-removed tag reported a change")
	}
	tags, _ = db.ListSessionTags(HintScopeInstance, "i1")
	if len(tags) != 1 || tags[0].Tag != "auth" {
		t.Fatalf("tags after remove = %+v; want [auth]", tags)
	}
	// Re-adding a removed tag revives it (soft delete is not permanent).
	if err := db.AddSessionTag(HintScopeInstance, "i1", "flaky", HintSourceAnnotate); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	tags, _ = db.ListSessionTags(HintScopeInstance, "i1")
	if len(tags) != 2 {
		t.Fatalf("tags after re-add = %+v; want 2", tags)
	}
	if err := db.AddSessionTag(HintScopeInstance, "i1", "has space", HintSourceAnnotate); err == nil {
		t.Fatal("tag with whitespace accepted; want error")
	}
}

// Instances are hard-deleted (DeleteInstance) and bulk-pruned (the sweep in
// SaveInstances). Both must cascade to instance-scoped hints, tags and
// links, otherwise orphans accumulate forever in the one database that is
// never wiped.
func TestRecallRows_PruneCascadesWithInstance(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "keep", nil)
	seedInstance(t, db, "gone", nil)
	seedInstance(t, db, "gone2", nil)
	for _, id := range []string{"keep", "gone", "gone2"} {
		if err := db.SetSessionHint(HintScopeInstance, id, "purpose", "p-"+id, HintSourceCLICreate, ""); err != nil {
			t.Fatal(err)
		}
		if err := db.AddSessionTag(HintScopeInstance, id, "t-"+id, HintSourceCLICreate); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertSessionLink(id, "claude", "native-"+id, "", true); err != nil {
			t.Fatal(err)
		}
	}
	// Harness-scoped hints are keyed by native id, not instance id: they are
	// not orphaned by instance removal and must survive.
	if err := db.SetSessionHint(HintScopeHarnessSession, "native-gone", "topic", "kept", HintSourceHook, ""); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteInstance("gone"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	// Sweep save with only "keep" prunes "gone2".
	now := time.Now()
	row := &InstanceRow{ID: "keep", Title: "keep", Status: "idle", Tool: "shell", CreatedAt: now, LastAccessed: now, GroupPath: "default", ToolData: json.RawMessage(`{}`)}
	if err := db.SaveInstances([]*InstanceRow{row}); err != nil {
		t.Fatalf("SaveInstances sweep: %v", err)
	}

	count := func(table, id string) int {
		var n int
		if err := db.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE scope_id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count %s %s: %v", table, id, err)
		}
		return n
	}
	for _, id := range []string{"gone", "gone2"} {
		if n := count("session_hints", id); n != 0 {
			t.Errorf("session_hints for %s = %d; want 0 after prune", id, n)
		}
		if n := count("session_tags", id); n != 0 {
			t.Errorf("session_tags for %s = %d; want 0 after prune", id, n)
		}
		var links int
		if err := db.DB().QueryRow(`SELECT count(*) FROM session_links WHERE session_id = ?`, id).Scan(&links); err != nil || links != 0 {
			t.Errorf("session_links for %s = %d (err %v); want 0 after prune", id, links, err)
		}
	}
	if n := count("session_hints", "keep"); n != 1 {
		t.Errorf("session_hints for keep = %d; want 1", n)
	}
	if n := count("session_hints", "native-gone"); n != 1 {
		t.Errorf("harness_session hint for native-gone = %d; want 1 (not instance-scoped)", n)
	}
	if err := db.ClearAllInstances(); err != nil {
		t.Fatal(err)
	}
	if n := count("session_hints", "keep"); n != 0 {
		t.Errorf("ClearAllInstances left %d instance hints", n)
	}
}

// Both binding writers record the mapping they already know into
// session_links; a rebind demotes the previous native id to non-authoritative
// and a retraction removes the candidate row.
func TestSessionLinks_WrittenByBindingWriters(t *testing.T) {
	db := openTestDB(t)
	seedInstance(t, db, "i1", json.RawMessage(`{}`))
	seedInstance(t, db, "i2", json.RawMessage(`{}`))

	t0 := time.Unix(1_700_000_000, 0)
	if err := db.WriteClaudeSessionBinding("i1", "claude-a", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteClaudeSessionBinding("i1", "claude-a", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteClaudeSessionBinding("i1", "claude-b", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteGenericSessionBinding("i2", "gen-1", "mytool", "mytool --resume", "local", t0); err != nil {
		t.Fatal(err)
	}

	links, err := db.ListSessionLinks("i1")
	if err != nil {
		t.Fatalf("ListSessionLinks: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("links for i1 = %+v; want 2 (a demoted, b authoritative)", links)
	}
	byNative := map[string]SessionLink{}
	for _, l := range links {
		byNative[l.NativeID] = l
	}
	if a := byNative["claude-a"]; a.Harness != "claude" || a.Authoritative || a.FirstSeen != t0.Unix() || a.LastSeen != t0.Add(time.Minute).Unix() {
		t.Errorf("claude-a link = %+v; want demoted, first_seen t0, last_seen t0+1m", a)
	}
	if b := byNative["claude-b"]; !b.Authoritative {
		t.Errorf("claude-b link = %+v; want authoritative", b)
	}
	gen, _ := db.ListSessionLinks("i2")
	if len(gen) != 1 || gen[0].Harness != "mytool" || gen[0].NativeID != "gen-1" || !gen[0].Authoritative {
		t.Fatalf("generic link = %+v; want mytool/gen-1 authoritative", gen)
	}

	// Clearing the generic binding retracts its authority.
	if err := db.WriteGenericSessionBinding("i2", "", "", "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	gen, _ = db.ListSessionLinks("i2")
	if len(gen) != 1 || gen[0].Authoritative {
		t.Fatalf("generic link after clear = %+v; want demoted", gen)
	}

	// Adoption rejection retracts the candidate's link row.
	removed, err := db.RetractSessionLink("i1", "claude", "claude-a")
	if err != nil || !removed {
		t.Fatalf("RetractSessionLink = %v, %v; want true, nil", removed, err)
	}
	links, _ = db.ListSessionLinks("i1")
	if len(links) != 1 || links[0].NativeID != "claude-b" {
		t.Fatalf("links after retract = %+v; want only claude-b", links)
	}
	if removed, _ := db.RetractSessionLink("i1", "claude", "never-bound"); removed {
		t.Fatal("retracting an unknown candidate reported a change")
	}
}

func TestAuthoritativeLinkOwner_OnlyAuthoritativeRows(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertSessionLink("inst-1", "claude", "conv-old", "", true); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSessionLink("inst-1", "claude", "conv-new", "", true); err != nil {
		t.Fatal(err)
	}
	owner, err := db.AuthoritativeLinkOwner("claude", "conv-new")
	if err != nil || owner != "inst-1" {
		t.Fatalf("owner = %q %v", owner, err)
	}
	// conv-old was demoted by the rebind: history, not a binding.
	if owner, _ := db.AuthoritativeLinkOwner("claude", "conv-old"); owner != "" {
		t.Fatalf("demoted link still binds: %q", owner)
	}
	if owner, _ := db.AuthoritativeLinkOwner("codex", "conv-new"); owner != "" {
		t.Fatalf("harness not honoured: %q", owner)
	}
}

func TestRecallChangedRefs_HintsTagsAndLinks(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertSessionLink("inst-1", "claude", "conv-1", "", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionHint(HintScopeInstance, "inst-1", "ticket", "SB-1", HintSourceAnnotate, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionHint(HintScopeHarnessSession, "conv-9", "why", "x", HintSourceAnnotate, ""); err != nil {
		t.Fatal(err)
	}
	refs, err := db.RecallChangedRefs(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[HarnessRef]bool{}
	for _, r := range refs {
		got[r] = true
	}
	if len(got) != 2 || !got[HarnessRef{"claude", "conv-1"}] || !got[HarnessRef{"", "conv-9"}] {
		t.Fatalf("refs = %v", refs)
	}
	if refs, _ := db.RecallChangedRefs(time.Now().Unix() + 3600); len(refs) != 0 {
		t.Fatalf("future cutoff returned %v", refs)
	}
}
