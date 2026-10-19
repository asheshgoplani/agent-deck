package session

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestStorageRefusesAnotherProfilesStore: a writable open of a profile whose
// state.db belongs to another profile (a copied or restored store) is an
// error before anything is written. A read-only open of it succeeds with a
// warning and writes nothing. A store from before the stamp existed is
// stamped on its first open and keeps its sessions.
func TestStorageRefusesAnotherProfilesStore(t *testing.T) {
	suffix := randomString(8)
	work, personal, legacy := "_test_ident_work_"+suffix, "_test_ident_personal_"+suffix, "_test_ident_legacy_"+suffix

	ws, err := NewStorageWithProfile(work)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(work): %v", err)
	}
	inst := NewInstanceWithTool("identity", t.TempDir(), "shell")
	inst.GroupPath = DefaultGroupPath
	if err := ws.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst})); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	workPath := ws.Path()
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	personalPath, err := GetDBPathForProfile(personal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(personalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(workPath, personalPath); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, personalPath)

	if s, err := NewStorageWithProfile(personal); !errors.Is(err, statedb.ErrProfileMismatch) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("NewStorageWithProfile on another profile's store: err = %v, want ErrProfileMismatch", err)
	}
	// A read-only open never writes, so a mismatch only warns: readers (the
	// statusline, the remote catalog) are never locked out by a stamp.
	for name, open := range map[string]func(string) (*Storage, error){
		"NewReadOnlyStorageWithProfile":     NewReadOnlyStorageWithProfile,
		"NewLiveReadOnlyStorageWithProfile": NewLiveReadOnlyStorageWithProfile,
	} {
		s, err := open(personal)
		if err != nil {
			t.Fatalf("%s on another profile's store: %v; a read-only open must warn, not refuse", name, err)
		}
		_ = s.Close()
	}
	if after := fileBytes(t, personalPath); string(after) != string(before) {
		t.Fatal("a refused open changed the store file")
	}

	// Pre-stamp store: stamped on first open, sessions kept.
	legacyPath, err := GetDBPathForProfile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := statedb.Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Migrate(); err != nil {
		t.Fatal(err)
	}
	row, err := instanceToRow(inst)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	ls, err := NewStorageWithProfile(legacy)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(unstamped legacy store): %v", err)
	}
	defer ls.Close()
	if stamp, _ := ls.GetDB().GetMeta("profile"); stamp != legacy {
		t.Fatalf("legacy store stamp = %q, want %q", stamp, legacy)
	}
	if got, _, err := ls.LoadWithGroups(); err != nil || len(got) != 1 || got[0].ID != inst.ID {
		t.Fatalf("legacy store lost its session after stamping: n=%d err=%v", len(got), err)
	}
}

// TestTargetedWritesLandInOwningProfile drives every targeted write an
// instance makes outside a full save with the process-global database set to
// ANOTHER profile, the way a TUI or web process for one profile holds sessions
// loaded from another. The same session id exists in both stores, so a write
// that goes to the global database is visible instead of a silent zero-row
// UPDATE. Each write must land in the profile the instance was loaded from and
// leave the other profile untouched: while the owning handle is open; after it
// was closed (a short-lived Storage, as web mutators use); after it was closed
// while the global handle is another handle on the owning file; and with no
// global handle at all (CLI processes), where an owned instance still writes
// to its own store.
func TestTargetedWritesLandInOwningProfile(t *testing.T) {
	type writePoint struct {
		name string
		// seed prepares the instance (before it is saved to both stores) and
		// each store's rows; both are optional.
		seed   func(inst *Instance)
		seedDB func(t *testing.T, db *statedb.StateDB, id string)
		write  func(t *testing.T, inst *Instance)
		// read returns what the write changes, as stored.
		read func(t *testing.T, db *sql.DB, id string) string
	}
	col := func(query string) func(t *testing.T, db *sql.DB, id string) string {
		return func(t *testing.T, db *sql.DB, id string) string {
			t.Helper()
			var v sql.NullString
			if err := db.QueryRow(query, id).Scan(&v); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			return v.String
		}
	}
	const candidate = "77777777-7777-4777-8777-777777777777"
	points := []writePoint{
		{name: "MarkAccessed", write: func(t *testing.T, inst *Instance) { inst.MarkAccessed() },
			read: col(`SELECT CAST(last_accessed AS TEXT) FROM instances WHERE id = ?`)},
		{name: "claude binding", write: func(t *testing.T, inst *Instance) {
			inst.bindClaudeSessionFromHook("11111111-1111-4111-8111-111111111111", "test", "SessionStart", "test")
		}, read: col(`SELECT json_extract(tool_data, '$.claude_session_id') FROM instances WHERE id = ?`)},
		{name: "codex binding", write: func(t *testing.T, inst *Instance) {
			if err := inst.bindCodexSessionFromHook("22222222-2222-4222-8222-222222222222", "SessionStart"); err != nil {
				t.Fatalf("bindCodexSessionFromHook: %v", err)
			}
		}, read: col(`SELECT json_extract(tool_data, '$.codex_session_id') FROM instances WHERE id = ?`)},
		{name: "gemini binding", write: func(t *testing.T, inst *Instance) {
			inst.bindGeminiSessionFromHook("33333333-3333-4333-8333-333333333333", "SessionStart")
		}, read: col(`SELECT json_extract(tool_data, '$.gemini_session_id') FROM instances WHERE id = ?`)},
		{name: "hermes binding", write: func(t *testing.T, inst *Instance) {
			inst.persistHermesSessionBinding("hermes-44444444")
		}, read: col(`SELECT json_extract(tool_data, '$.hermes_session_id') FROM instances WHERE id = ?`)},
		{name: "claude session link", write: func(t *testing.T, inst *Instance) {
			inst.confirmClaudeSessionLink("55555555-5555-4555-8555-555555555555", "test")
		}, read: col(`SELECT CAST(COUNT(*) AS TEXT) FROM session_links WHERE session_id = ?`)},
		{name: "claude candidate link retract",
			seedDB: func(t *testing.T, db *statedb.StateDB, id string) {
				if err := db.UpsertSessionLink(id, "claude", candidate, "", false); err != nil {
					t.Fatalf("seed link: %v", err)
				}
			},
			write: func(t *testing.T, inst *Instance) { inst.retractClaudeCandidateLink(candidate) },
			read: col(`SELECT CAST(COUNT(*) AS TEXT) FROM session_links WHERE session_id = ? AND native_id = '` +
				candidate + `'`)},
		{name: "tool-session-id mutator", write: func(t *testing.T, inst *Instance) {
			if _, _, err := SetField(inst, FieldToolSessionID, "generic-66666666", nil); err != nil {
				t.Fatalf("SetField(tool-session-id): %v", err)
			}
		}, read: col(`SELECT json_extract(tool_data, '$.generic_session_id') FROM instances WHERE id = ?`)},
		{name: "fresh-start clear",
			seed: func(inst *Instance) {
				inst.GenericSessionID = "generic-88888888"
				inst.GenericDetectedAt = time.Now()
			},
			write: func(t *testing.T, inst *Instance) {
				inst.clearSessionBindingForFreshStart()
				if err := inst.GenericSessionPersistError(); err != nil {
					t.Fatalf("fresh-start clear: %v", err)
				}
			},
			read: col(`SELECT json_extract(tool_data, '$.generic_session_id') FROM instances WHERE id = ?`)},
		{name: "restart outcome", write: func(t *testing.T, inst *Instance) {
			inst.tmuxSession = tmux.ReconnectSessionLazy("agentdeck_identity_new_"+randomString(6), inst.ID, inst.ProjectPath, "shell", "running")
			if _, err := inst.writeRestartOutcome(); err != nil {
				t.Fatalf("writeRestartOutcome: %v", err)
			}
		}, read: col(`SELECT tmux_session FROM instances WHERE id = ?`)},
	}

	type mode struct {
		name string
		// closeOwner closes the owning handle before the write.
		closeOwner bool
		// global: "other" (another profile's store), "same" (a second
		// handle on the owning file) or "none".
		global string
	}
	modes := []mode{
		{name: "", global: "other"},
		{name: "owner handle closed", closeOwner: true, global: "other"},
		{name: "owner handle closed, global is the owning file", closeOwner: true, global: "same"},
		{name: "no global handle", global: "none"},
	}

	for _, m := range modes {
		for _, p := range points {
			name := p.name
			if m.name != "" {
				name += "/" + m.name
			}
			t.Run(name, func(t *testing.T) {
				suffix := randomString(8)
				owner, err := NewStorageWithProfile("_test_owner_" + suffix)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = owner.Close() })
				other, err := NewStorageWithProfile("_test_global_" + suffix)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = other.Close() })
				prev := statedb.GetGlobal()
				t.Cleanup(func() { statedb.SetGlobal(prev) })
				switch m.global {
				case "other":
					statedb.SetGlobal(other.GetDB())
				case "same":
					same, err := statedb.Open(owner.Path())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = same.Close() })
					statedb.SetGlobal(same)
				default:
					statedb.SetGlobal(nil)
				}

				seed := NewInstanceWithTool("identity", t.TempDir(), "shell")
				seed.GroupPath = DefaultGroupPath
				seed.tmuxSession = tmux.ReconnectSessionLazy("agentdeck_identity_old", seed.ID, seed.ProjectPath, "shell", "running")
				if p.seed != nil {
					p.seed(seed)
				}
				for _, s := range []*Storage{other, owner} {
					if err := s.SaveWithGroups([]*Instance{seed}, NewGroupTree([]*Instance{seed})); err != nil {
						t.Fatalf("seed save: %v", err)
					}
					if p.seedDB != nil {
						p.seedDB(t, s.GetDB(), seed.ID)
					}
				}
				loaded, _, err := owner.LoadWithGroups()
				if err != nil || len(loaded) != 1 {
					t.Fatalf("load from owner: n=%d err=%v", len(loaded), err)
				}
				inst := loaded[0]

				ownerPath, otherDB := owner.Path(), other.GetDB().DB()
				ownerBefore := p.read(t, owner.GetDB().DB(), inst.ID)
				otherBefore := p.read(t, otherDB, inst.ID)
				if m.closeOwner {
					if err := owner.Close(); err != nil {
						t.Fatal(err)
					}
				}

				p.write(t, inst)

				check, err := statedb.Open(ownerPath)
				if err != nil {
					t.Fatal(err)
				}
				defer check.Close()
				if got := p.read(t, check.DB(), inst.ID); got == ownerBefore {
					t.Errorf("owning profile not written: still %q", got)
				}
				if got := p.read(t, otherDB, inst.ID); got != otherBefore {
					t.Errorf("write landed in the other profile's store: %q -> %q", otherBefore, got)
				}
			})
		}
	}
}

// TestProfileAliasDoesNotLockOutTheRealName: a profile directory reached
// through a symlink (profiles/alias -> profiles/real) is one profile. An
// unstamped store opened through the alias first is stamped with the real
// directory's name, so the real name, the alias and every read-only open
// keep working afterwards.
func TestProfileAliasDoesNotLockOutTheRealName(t *testing.T) {
	suffix := randomString(8)
	target, alias := "_test_alias_real_"+suffix, "_test_alias_link_"+suffix
	realDir, err := GetProfileDir(target)
	if err != nil {
		t.Fatal(err)
	}
	aliasDir, err := GetProfileDir(alias)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	// An unstamped store, as every store from before the stamp is.
	raw, err := statedb.Open(filepath.Join(realDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Migrate(); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	as, err := NewStorageWithProfile(alias)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(alias): %v", err)
	}
	stamp, _ := as.GetDB().GetMeta("profile")
	_ = as.Close()
	if stamp != target {
		t.Fatalf("store opened through the alias stamped %q, want the real directory's name %q", stamp, target)
	}
	for _, p := range []string{target, alias} {
		ws, err := NewStorageWithProfile(p)
		if err != nil {
			t.Fatalf("NewStorageWithProfile(%q) after the alias stamped the store: %v", p, err)
		}
		_ = ws.Close()
		ro, err := NewReadOnlyStorageWithProfile(p)
		if err != nil {
			t.Fatalf("NewReadOnlyStorageWithProfile(%q): %v", p, err)
		}
		if got := ro.GetDB().Profile(); !strings.EqualFold(got, target) {
			t.Fatalf("read-only open of %q confirmed profile %q, want %q", p, got, target)
		}
		_ = ro.Close()
	}
}

// TestOwningWriteRetriesWhenTheHandleClosesMidWrite: a web mutator's
// short-lived Storage can close the owning handle between the liveness check
// and the write. The write is then retried through the saved owning path, as
// it was before the helper existed, instead of failing.
func TestOwningWriteRetriesWhenTheHandleClosesMidWrite(t *testing.T) {
	suffix := randomString(8)
	owner, err := NewStorageWithProfile("_test_race_owner_" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	other, err := NewStorageWithProfile("_test_race_global_" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	prev := statedb.GetGlobal()
	statedb.SetGlobal(other.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	seed := NewInstanceWithTool("race", t.TempDir(), "codex")
	seed.GroupPath = DefaultGroupPath
	if err := owner.SaveWithGroups([]*Instance{seed}, NewGroupTree([]*Instance{seed})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := owner.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: n=%d err=%v", len(loaded), err)
	}
	inst := loaded[0]
	ownerPath := owner.Path()

	const sid = "99999999-9999-4999-8999-999999999999"
	calls := 0
	err = inst.withOwningStateDB(func(db *statedb.StateDB) error {
		calls++
		if calls == 1 {
			// The short-lived Storage closes between the ping and the write.
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return db.WriteCodexSessionBinding(inst.ID, sid, time.Now())
	})
	if err != nil {
		t.Fatalf("write after the owning handle closed mid-write: %v (calls=%d)", err, calls)
	}
	check, err := statedb.Open(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var got sql.NullString
	if err := check.DB().QueryRow(`SELECT json_extract(tool_data, '$.codex_session_id') FROM instances WHERE id = ?`, inst.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.String != sid {
		t.Fatalf("owning store codex_session_id = %q, want %q", got.String, sid)
	}
}

// TestOwningWriteNeverResurrectsADeletedProfile: after `profile delete`
// removed the owning profile's directory, a targeted write from an instance
// still in memory fails instead of recreating the directory and an empty
// state.db (which would bring the profile back in ListProfiles).
func TestOwningWriteNeverResurrectsADeletedProfile(t *testing.T) {
	suffix := randomString(8)
	gone := "_test_deleted_owner_" + suffix
	owner, err := NewStorageWithProfile(gone)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewStorageWithProfile("_test_deleted_global_" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	prev := statedb.GetGlobal()
	statedb.SetGlobal(other.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	seed := NewInstanceWithTool("deleted", t.TempDir(), "codex")
	seed.GroupPath = DefaultGroupPath
	if err := owner.SaveWithGroups([]*Instance{seed}, NewGroupTree([]*Instance{seed})); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := owner.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: n=%d err=%v", len(loaded), err)
	}
	inst := loaded[0]
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := GetProfileDir(gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := inst.bindCodexSessionFromHook("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "SessionStart"); err == nil {
		t.Error("codex binding for a deleted profile reported success")
	}
	inst.MarkAccessed()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deleted profile directory came back (stat err = %v)", err)
	}
	profiles, err := ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p == gone {
			t.Fatalf("ListProfiles lists the deleted profile %q again", gone)
		}
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
