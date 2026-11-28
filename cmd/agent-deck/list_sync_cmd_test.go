package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// listSyncLive is the fixture's one live session (sessionFixture: a waiting
// row whose fake pane shows a working spinner).
const listSyncLive = "list-perf-000"

type listSyncFixture struct {
	sb  *harness.Sandbox
	env []string
	db  string
}

// newListSyncFixture seeds one live session, two stopped ones (s1, s2) and
// two archived ones (a1, a2). The stopped and archived rows have no tmux
// session, so their rows only change when the test changes them.
func newListSyncFixture(t *testing.T) *listSyncFixture {
	t.Helper()
	sb, env, _ := sessionFixture(t, "codex")
	f := &listSyncFixture{sb: sb, env: env, db: filepath.Join(sb.Home, ".agent-deck", "profiles", "default", "state.db")}
	f.mutate(t, func(db *statedb.StateDB) error {
		return db.UpsertInstances([]*statedb.InstanceRow{
			listSyncRow("s1", 1, false), listSyncRow("s2", 2, false),
			listSyncRow("a1", 3, true), listSyncRow("a2", 4, true),
		})
	})
	return f
}

func listSyncRow(id string, order int, archived bool) *statedb.InstanceRow {
	row := &statedb.InstanceRow{
		ID: id, Title: "title " + id, ProjectPath: "/tmp/" + id, GroupPath: "my-sessions", Order: order,
		Tool: "shell", Command: "bash", Status: "stopped",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, order, 0, time.UTC), ToolData: json.RawMessage(`{}`),
	}
	if archived {
		row.ArchivedAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	}
	return row
}

func (f *listSyncFixture) mutate(t *testing.T, fn func(*statedb.StateDB) error) {
	t.Helper()
	db, err := statedb.Open(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := fn(db); err != nil {
		t.Fatal(err)
	}
}

// edit loads row id, changes it and stores it back.
func (f *listSyncFixture) edit(t *testing.T, id string, fn func(*statedb.InstanceRow)) {
	t.Helper()
	f.mutate(t, func(db *statedb.StateDB) error {
		rows, err := db.LoadInstances()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == id {
				fn(row)
				return db.UpsertInstances([]*statedb.InstanceRow{row})
			}
		}
		return fmt.Errorf("no row %s", id)
	})
}

// stopLive stops the live session as `session stop` leaves it: stored
// status stopped and its tmux session gone.
func (f *listSyncFixture) stopLive(t *testing.T) {
	t.Helper()
	for _, name := range []string{"names", "windows", "panes"} {
		if err := os.WriteFile(filepath.Join(f.sb.Home, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.edit(t, listSyncLive, func(r *statedb.InstanceRow) { r.Status = "stopped" })
}

func (f *listSyncFixture) journal() string {
	return filepath.Join(filepath.Dir(f.db), session.RemoteSyncJournalName)
}

func (f *listSyncFixture) sync(t *testing.T, args ...string) session.RemoteSyncEnvelope {
	t.Helper()
	res := runListCapture(t, f.sb, f.env, append([]string{"list", "--json", "--sync"}, args...)...)
	out := strings.TrimSuffix(res.stdout, "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("sync reply is not one line: %q", res.stdout)
	}
	var env session.RemoteSyncEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("sync reply %q: %v", res.stdout, err)
	}
	if env.Sync != session.RemoteSyncVersion || env.Sessions == nil || env.Removed == nil {
		t.Fatalf("bad envelope: %s", out)
	}
	return env
}

func listSyncIDs(env session.RemoteSyncEnvelope) []string {
	ids := make([]string, 0, len(env.Sessions))
	for _, r := range env.Sessions {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

func listSyncRowByID(env session.RemoteSyncEnvelope, id string) *session.RemoteSyncRow {
	for i := range env.Sessions {
		if env.Sessions[i].ID == id {
			return &env.Sessions[i]
		}
	}
	return nil
}

// sameIDs compares got to want; the live row may also appear when allowLive
// is set, since its pane activity can move between two polls.
func sameIDs(got, want []string, allowLive bool) bool {
	if allowLive {
		got = slices.DeleteFunc(slices.Clone(got), func(id string) bool { return id == listSyncLive && !slices.Contains(want, listSyncLive) })
	}
	w := slices.Clone(want)
	sort.Strings(w)
	return slices.Equal(got, w)
}

// A full sync lists exactly the rows in scope, with the values a legacy
// `list --json` decode gives and the instance order, and counts both scopes.
func TestListSync_FullScopesAndCounts(t *testing.T) {
	f := newListSyncFixture(t)
	for _, tc := range []struct {
		scope string
		want  []string
	}{
		{"active", []string{listSyncLive, "s1", "s2"}},
		{"archived", []string{"a1", "a2"}},
		{"all", []string{listSyncLive, "a1", "a2", "s1", "s2"}},
		{"", []string{listSyncLive, "a1", "a2", "s1", "s2"}},
	} {
		args := []string{}
		if tc.scope != "" {
			args = append(args, "--scope", tc.scope)
		}
		env := f.sync(t, args...)
		if !env.Full || env.More || env.Next != "" || len(env.Removed) != 0 {
			t.Fatalf("scope %q: want one full page, got %+v", tc.scope, env)
		}
		if got := listSyncIDs(env); !sameIDs(got, tc.want, false) {
			t.Fatalf("scope %q: rows %v, want %v", tc.scope, got, tc.want)
		}
		if env.Counts != (session.RemoteSyncCounts{Active: 3, Archived: 2}) {
			t.Fatalf("scope %q: counts %+v", tc.scope, env.Counts)
		}
		if !strings.HasPrefix(env.Cursor, env.Epoch+".") {
			t.Fatalf("scope %q: cursor %q is not of epoch %q", tc.scope, env.Cursor, env.Epoch)
		}
		wantScope := tc.scope
		if wantScope == "" {
			wantScope = "all"
		}
		if env.Scope != wantScope {
			t.Fatalf("scope %q: envelope scope %q", tc.scope, env.Scope)
		}
	}

	// Values: every stopped and archived row equals its legacy decode.
	legacy := runListCapture(t, f.sb, f.env, "list", "--json")
	var infos []session.RemoteSessionInfo
	if err := json.Unmarshal([]byte(legacy.stdout), &infos); err != nil {
		t.Fatal(err)
	}
	env := f.sync(t)
	for _, info := range infos {
		row := listSyncRowByID(env, info.ID)
		if row == nil {
			t.Fatalf("row %s missing from the sync reply", info.ID)
		}
		if info.ID == listSyncLive {
			continue // its activity time moves between the two calls
		}
		a, _ := json.Marshal(info)
		b, _ := json.Marshal(row.RemoteSessionInfo)
		if string(a) != string(b) {
			t.Fatalf("row %s differs from the legacy decode:\nsync:   %s\nlegacy: %s", info.ID, b, a)
		}
	}
	if r := listSyncRowByID(env, "a2"); r.Order != 4 || !r.Archived {
		t.Fatalf("a2 = %+v, want order 4 and archived", r)
	}
}

// After a full sync, each change arrives as a delta in every scope's own
// cursor: upserts for in-scope rows, removals for rows deleted or moved out
// of the scope.
func TestListSync_DeltaAfterChanges(t *testing.T) {
	f := newListSyncFixture(t)
	cursors := map[string]string{
		"active":   f.sync(t, "--scope", "active").Cursor,
		"archived": f.sync(t, "--scope", "archived").Cursor,
	}
	type want struct{ sessions, removed []string }
	steps := []struct {
		name     string
		change   func()
		active   want
		archived want
		check    func(session.RemoteSyncEnvelope)
	}{
		{name: "nothing", change: func() {}},
		{
			name: "add",
			change: func() {
				f.mutate(t, func(db *statedb.StateDB) error {
					return db.UpsertInstances([]*statedb.InstanceRow{listSyncRow("s3", 5, false)})
				})
			},
			active: want{sessions: []string{"s3"}},
		},
		{
			name:   "rename",
			change: func() { f.edit(t, "s1", func(r *statedb.InstanceRow) { r.Title = "renamed" }) },
			active: want{sessions: []string{"s1"}},
			check: func(env session.RemoteSyncEnvelope) {
				if r := listSyncRowByID(env, "s1"); r == nil || r.Title != "renamed" {
					t.Fatalf("rename: s1 = %+v", r)
				}
			},
		},
		{
			name:   "stop",
			change: func() { f.stopLive(t) },
			active: want{sessions: []string{listSyncLive}},
			check: func(env session.RemoteSyncEnvelope) {
				if r := listSyncRowByID(env, listSyncLive); r == nil || r.Status == "running" || r.Status == "waiting" {
					t.Fatalf("stop: %s = %+v", listSyncLive, r)
				}
			},
		},
		{
			name:     "archive",
			change:   func() { f.edit(t, "s2", func(r *statedb.InstanceRow) { r.ArchivedAt = time.Now() }) },
			active:   want{removed: []string{"s2"}},
			archived: want{sessions: []string{"s2"}},
		},
		{
			name:     "unarchive",
			change:   func() { f.edit(t, "a1", func(r *statedb.InstanceRow) { r.ArchivedAt = time.Time{} }) },
			active:   want{sessions: []string{"a1"}},
			archived: want{removed: []string{"a1"}},
		},
		{
			name:   "delete",
			change: func() { f.mutate(t, func(db *statedb.StateDB) error { return db.DeleteInstance("s3") }) },
			active: want{removed: []string{"s3"}},
			// A tombstone goes to every scope; dropping an id it never had
			// is a no-op for the client.
			archived: want{removed: []string{"s3"}},
		},
		{name: "nothing again", change: func() {}},
	}
	stopped := false
	for _, step := range steps {
		step.change()
		if step.name == "stop" {
			stopped = true
		}
		for _, scope := range []string{"active", "archived"} {
			w := step.active
			if scope == "archived" {
				w = step.archived
			}
			env := f.sync(t, "--scope", scope, "--cursor", cursors[scope])
			if env.Full {
				t.Fatalf("%s/%s: got a full resync for a valid cursor", step.name, scope)
			}
			if got := listSyncIDs(env); !sameIDs(got, w.sessions, scope == "active" && !stopped) {
				t.Fatalf("%s/%s: sessions %v, want %v", step.name, scope, got, w.sessions)
			}
			if got := slices.Sorted(slices.Values(env.Removed)); !sameIDs(got, w.removed, false) {
				t.Fatalf("%s/%s: removed %v, want %v", step.name, scope, got, w.removed)
			}
			if step.check != nil && scope == "active" {
				step.check(env)
			}
			cursors[scope] = env.Cursor
		}
	}
	if env := f.sync(t, "--scope", "active"); !sameIDs(listSyncIDs(env), []string{listSyncLive, "a1", "s1"}, false) {
		t.Fatalf("after the changes a full active sync lists %v", listSyncIDs(env))
	}
}

// A cursor the journal cannot answer gets a full resync: a lost or corrupt
// journal (new epoch), a cursor below the floor (a tombstone it needs was
// pruned), above the journal's seq, from another epoch, or malformed.
func TestListSync_FullResyncWhenCursorUnusable(t *testing.T) {
	f := newListSyncFixture(t)
	first := f.sync(t, "--scope", "active")
	readJournal := func() map[string]any {
		data, err := os.ReadFile(f.journal())
		if err != nil {
			t.Fatal(err)
		}
		var j map[string]any
		if err := json.Unmarshal(data, &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	writeJournal := func(j map[string]any) {
		data, _ := json.Marshal(j)
		if err := os.WriteFile(f.journal(), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seq := uint64(readJournal()["seq"].(float64))

	for _, tc := range []struct {
		name      string
		prepare   func()
		cursor    string
		sameEpoch bool
	}{
		{name: "above seq", cursor: fmt.Sprintf("%s.%d", first.Epoch, seq+100), sameEpoch: true},
		{name: "other epoch", cursor: "feedface." + fmt.Sprint(seq), sameEpoch: true},
		{name: "malformed", cursor: "garbage", sameEpoch: true},
		{name: "no cursor", cursor: "", sameEpoch: true},
		{
			name: "below floor",
			prepare: func() {
				j := readJournal()
				j["floor"] = float64(seq)
				writeJournal(j)
			},
			cursor:    fmt.Sprintf("%s.%d", first.Epoch, seq-1),
			sameEpoch: true,
		},
		{
			name:    "journal deleted",
			prepare: func() { _ = os.Remove(f.journal()) },
			cursor:  first.Cursor,
		},
		{
			name: "journal corrupt",
			prepare: func() {
				if err := os.WriteFile(f.journal(), []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			cursor: first.Cursor,
		},
	} {
		if tc.prepare != nil {
			tc.prepare()
		}
		args := []string{"--scope", "active"}
		if tc.cursor != "" {
			args = append(args, "--cursor", tc.cursor)
		}
		env := f.sync(t, args...)
		if !env.Full || !sameIDs(listSyncIDs(env), []string{listSyncLive, "s1", "s2"}, false) {
			t.Fatalf("%s: want a full resync of the active rows, got full=%v rows=%v", tc.name, env.Full, listSyncIDs(env))
		}
		if tc.sameEpoch != (env.Epoch == first.Epoch) {
			t.Fatalf("%s: epoch %q, first %q", tc.name, env.Epoch, first.Epoch)
		}
		// The resync's cursor is good for a delta.
		if next := f.sync(t, "--scope", "active", "--cursor", env.Cursor); next.Full {
			t.Fatalf("%s: the resync's own cursor was not accepted", tc.name)
		}
	}
}

// A paged full resync ends on the base cursor, so a change made between two
// pages (a sent row renamed, an unsent row deleted) comes in the next delta,
// and the pages plus the delta add up to the current listing. A delta is
// paged too, each page's cursor resuming after its last entry.
func TestListSync_Paging(t *testing.T) {
	f := newListSyncFixture(t)
	f.stopLive(t) // keep every row still between the calls
	_ = f.sync(t)

	held := map[string]session.RemoteSyncRow{}
	page := f.sync(t, "--limit", "2")
	if !page.Full || page.Next == "" || page.Cursor != "" || len(page.Sessions) != 2 {
		t.Fatalf("page 1: %+v", page)
	}
	for _, r := range page.Sessions {
		held[r.ID] = r
	}
	sent := page.Sessions[0].ID
	var unsent string
	for _, id := range []string{listSyncLive, "s1", "s2", "a1", "a2"} {
		if _, ok := held[id]; !ok {
			unsent = id
			break
		}
	}
	f.edit(t, sent, func(r *statedb.InstanceRow) { r.Title = "renamed while paging" })
	f.mutate(t, func(db *statedb.StateDB) error { return db.DeleteInstance(unsent) })

	cursor := ""
	for n := 2; ; n++ {
		if n > 10 {
			t.Fatal("paging did not end")
		}
		page = f.sync(t, "--limit", "2", "--page", page.Next)
		if !page.Full {
			t.Fatalf("page %d is not part of the full resync: %+v", n, page)
		}
		for _, r := range page.Sessions {
			held[r.ID] = r
		}
		if page.Next == "" {
			cursor = page.Cursor
			break
		}
	}
	delta := f.sync(t, "--cursor", cursor)
	if delta.Full {
		t.Fatal("the final page's cursor was not accepted")
	}
	for _, r := range delta.Sessions {
		held[r.ID] = r
	}
	for _, id := range delta.Removed {
		delete(held, id)
	}
	full := f.sync(t)
	if len(held) != len(full.Sessions) {
		t.Fatalf("pages+delta hold %d rows, the listing has %d", len(held), len(full.Sessions))
	}
	for _, r := range full.Sessions {
		if h, ok := held[r.ID]; !ok || h.Title != r.Title {
			t.Fatalf("row %s: held %+v, current %+v", r.ID, h, r)
		}
	}
	if !slices.Contains(delta.Removed, unsent) {
		t.Fatalf("delta after paging does not remove %s: %+v", unsent, delta)
	}

	// Delta paging: three changes, one per page.
	cursor = full.Cursor
	var edited []string
	for _, id := range []string{"s1", "s2", "a2"} {
		if id == unsent {
			continue
		}
		edited = append(edited, id)
		f.edit(t, id, func(r *statedb.InstanceRow) { r.Title = "again " + id })
	}
	got := []string{}
	for n := 0; ; n++ {
		if n > 10 {
			t.Fatal("delta paging did not end")
		}
		d := f.sync(t, "--limit", "1", "--cursor", cursor)
		if d.Full || len(d.Sessions)+len(d.Removed) > 1 {
			t.Fatalf("delta page %d: %+v", n, d)
		}
		got = append(got, listSyncIDs(d)...)
		cursor = d.Cursor
		if !d.More {
			break
		}
	}
	sort.Strings(got)
	sort.Strings(edited)
	if !slices.Equal(got, edited) {
		t.Fatalf("delta pages carried %v, want %v", got, edited)
	}
}

// --focus refreshes only the named ids plus the hot set (stored running,
// waiting or starting, conductors, favourites), echoes the cursor, reports a
// named id that is gone in removed, and leaves the next delta correct.
func TestListSync_Focus(t *testing.T) {
	f := newListSyncFixture(t)
	f.edit(t, "s2", func(r *statedb.InstanceRow) { r.IsConductor = true })
	base := f.sync(t, "--scope", "active")

	f.edit(t, "s1", func(r *statedb.InstanceRow) { r.Title = "focused" })
	for _, tc := range []struct {
		name    string
		args    []string
		want    []string
		removed []string
	}{
		{"named and hot", []string{"--focus", "s1", "--cursor", base.Cursor}, []string{listSyncLive, "s1", "s2"}, nil},
		{"hot only", []string{"--focus", "", "--cursor", base.Cursor}, []string{listSyncLive, "s2"}, nil},
		{"gone id", []string{"--focus", "nope,s1", "--cursor", base.Cursor}, []string{listSyncLive, "s1", "s2"}, []string{"nope"}},
		{"archived id out of scope", []string{"--focus", "a1", "--cursor", base.Cursor}, []string{listSyncLive, "s2"}, []string{"a1"}},
		{"archived scope", []string{"--focus", "a1", "--scope", "archived", "--cursor", base.Cursor}, []string{"a1"}, nil},
	} {
		args := tc.args
		if !slices.Contains(args, "--scope") {
			args = append(args, "--scope", "active")
		}
		env := f.sync(t, args...)
		if env.Full || env.Cursor != base.Cursor || env.Epoch != base.Epoch {
			t.Fatalf("%s: a focus reply must echo the cursor, got full=%v cursor=%q epoch=%q", tc.name, env.Full, env.Cursor, env.Epoch)
		}
		if got := listSyncIDs(env); !sameIDs(got, tc.want, false) {
			t.Fatalf("%s: rows %v, want %v", tc.name, got, tc.want)
		}
		if !sameIDs(slices.Sorted(slices.Values(env.Removed)), tc.removed, false) {
			t.Fatalf("%s: removed %v, want %v", tc.name, env.Removed, tc.removed)
		}
	}
	if r := listSyncRowByID(f.sync(t, "--scope", "active", "--focus", "s1"), "s1"); r == nil || r.Title != "focused" {
		t.Fatalf("focus row s1 = %+v", r)
	}
	// The cursor kept from before the focus still gets the rename.
	delta := f.sync(t, "--scope", "active", "--cursor", base.Cursor)
	if delta.Full || listSyncRowByID(delta, "s1") == nil {
		t.Fatalf("delta after focus: %+v", delta)
	}
	// Without a journal, a focus pass answers and leaves creating it to
	// the next full pass.
	if err := os.Remove(f.journal()); err != nil {
		t.Fatal(err)
	}
	if env := f.sync(t, "--scope", "active", "--focus", "s1"); env.Epoch != "" || listSyncRowByID(env, "s1") == nil {
		t.Fatalf("focus without a journal: %+v", env)
	}
	if _, err := os.Stat(f.journal()); !os.IsNotExist(err) {
		t.Fatalf("a focus pass created the journal: %v", err)
	}
}

// Without --sync, `list --json` prints exactly what it printed before: a
// sync pass and its journal leave it alone, on both list implementations
// (the bytes themselves are pinned by the list_json golden).
func TestListSync_LegacyOutputUnchanged(t *testing.T) {
	f := newListSyncFixture(t)
	f.stopLive(t) // no live activity between the calls
	for _, legacy := range []bool{false, true} {
		env := f.env
		if legacy {
			env = append(slices.Clone(f.env), envCoreRegistry+"=0")
		}
		before := runListCapture(t, f.sb, env, "list", "--json").stdout
		_ = f.sync(t, "--scope", "active")
		if _, err := os.Stat(f.journal()); err != nil {
			t.Fatalf("no journal after a sync pass: %v", err)
		}
		after := runListCapture(t, f.sb, env, "list", "--json").stdout
		if before != after {
			t.Fatalf("legacy=%v: list --json changed after a sync pass:\nbefore: %s\nafter:  %s", legacy, before, after)
		}
		if !strings.HasPrefix(after, "[\n  {") || strings.Contains(after, `"order"`) {
			t.Fatalf("legacy=%v: list --json is no longer the indented array: %.80q", legacy, after)
		}
	}
}

// The sync flags are refused where they mean nothing, and both list
// implementations accept them.
func TestListSync_FlagMisuse(t *testing.T) {
	f := newListSyncFixture(t)
	for _, args := range [][]string{
		{"list", "--json", "--cursor", "x"},
		{"list", "--sync"},
		{"list", "--json", "--sync", "--scope", "bogus"},
		{"list", "--json", "--sync", "--limit", "-1"},
		{"list", "--json", "--sync", "--no-archived"},
	} {
		stdout, _, code, err := execAgentDeckEnv(f.sb.BinPath, f.sb.Home, "", f.env, args...)
		if err != nil {
			t.Fatal(err)
		}
		if code == 0 || !strings.Contains(stdout, "Error:") {
			t.Fatalf("%v: exit %d, stdout %q; want an error", args, code, stdout)
		}
	}
	legacyEnv := append(slices.Clone(f.env), envCoreRegistry+"=0")
	res := runListCapture(t, f.sb, legacyEnv, "list", "--json", "--sync", "--scope", "archived")
	var env session.RemoteSyncEnvelope
	if err := json.Unmarshal([]byte(res.stdout), &env); err != nil || !sameIDs(listSyncIDs(env), []string{"a1", "a2"}, false) {
		t.Fatalf("legacy path sync: %q (%v)", res.stdout, err)
	}
}

// The journal sits beside state.db and is not one of the files whose stamp
// the remote agent's change feed watches, so a sync poll through the agent
// (which writes the journal) never makes the agent push "changed".
func TestListSync_JournalOutsideAgentStamp(t *testing.T) {
	f := newListSyncFixture(t)
	_ = f.sync(t)
	if filepath.Dir(f.journal()) != filepath.Dir(f.db) {
		t.Fatalf("journal %s is not beside %s", f.journal(), f.db)
	}
	before := remoteAgentStamp(f.db)
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(f.journal(), []byte("rewritten"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.journal()+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if after := remoteAgentStamp(f.db); after != before {
		t.Fatalf("writing the journal moved the agent's stamp:\nbefore: %s\nafter:  %s", before, after)
	}
}
