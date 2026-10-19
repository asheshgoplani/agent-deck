package session

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestStorageRefusesAnotherProfilesStore: opening a profile whose state.db
// belongs to another profile (a copied or aliased store, or a path that
// resolved to the wrong directory) is an error before anything is written,
// for the writable and the read-only open alike. A store from before the
// stamp existed is stamped on its first open and keeps its sessions.
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
	if s, err := NewReadOnlyStorageWithProfile(personal); !errors.Is(err, statedb.ErrProfileMismatch) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("NewReadOnlyStorageWithProfile on another profile's store: err = %v, want ErrProfileMismatch", err)
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
// leave the other profile untouched, both while the owning handle is open and
// after it was closed (a short-lived Storage, as web mutators use).
func TestTargetedWritesLandInOwningProfile(t *testing.T) {
	type writePoint struct {
		name  string
		write func(t *testing.T, inst *Instance)
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
	points := []writePoint{
		{"MarkAccessed", func(t *testing.T, inst *Instance) { inst.MarkAccessed() },
			col(`SELECT CAST(last_accessed AS TEXT) FROM instances WHERE id = ?`)},
		{"claude binding", func(t *testing.T, inst *Instance) {
			inst.bindClaudeSessionFromHook("11111111-1111-4111-8111-111111111111", "test", "SessionStart", "test")
		}, col(`SELECT json_extract(tool_data, '$.claude_session_id') FROM instances WHERE id = ?`)},
		{"codex binding", func(t *testing.T, inst *Instance) {
			if err := inst.bindCodexSessionFromHook("22222222-2222-4222-8222-222222222222", "SessionStart"); err != nil {
				t.Fatalf("bindCodexSessionFromHook: %v", err)
			}
		}, col(`SELECT json_extract(tool_data, '$.codex_session_id') FROM instances WHERE id = ?`)},
		{"gemini binding", func(t *testing.T, inst *Instance) {
			inst.bindGeminiSessionFromHook("33333333-3333-4333-8333-333333333333", "SessionStart")
		}, col(`SELECT json_extract(tool_data, '$.gemini_session_id') FROM instances WHERE id = ?`)},
		{"hermes binding", func(t *testing.T, inst *Instance) {
			inst.persistHermesSessionBinding("hermes-44444444")
		}, col(`SELECT json_extract(tool_data, '$.hermes_session_id') FROM instances WHERE id = ?`)},
		{"claude session link", func(t *testing.T, inst *Instance) {
			inst.confirmClaudeSessionLink("55555555-5555-4555-8555-555555555555", "test")
		}, col(`SELECT CAST(COUNT(*) AS TEXT) FROM session_links WHERE session_id = ?`)},
		{"tool-session-id mutator", func(t *testing.T, inst *Instance) {
			if _, _, err := SetField(inst, FieldToolSessionID, "generic-66666666", nil); err != nil {
				t.Fatalf("SetField(tool-session-id): %v", err)
			}
		}, col(`SELECT json_extract(tool_data, '$.generic_session_id') FROM instances WHERE id = ?`)},
		{"restart outcome", func(t *testing.T, inst *Instance) {
			inst.tmuxSession = tmux.ReconnectSessionLazy("agentdeck_identity_new_"+randomString(6), inst.ID, inst.ProjectPath, "shell", "running")
			if _, err := inst.writeRestartOutcome(); err != nil {
				t.Fatalf("writeRestartOutcome: %v", err)
			}
		}, col(`SELECT tmux_session FROM instances WHERE id = ?`)},
	}

	for _, ownerClosed := range []bool{false, true} {
		for _, p := range points {
			name := p.name
			if ownerClosed {
				name += "/owner handle closed"
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
				statedb.SetGlobal(other.GetDB())
				t.Cleanup(func() { statedb.SetGlobal(prev) })

				seed := NewInstanceWithTool("identity", t.TempDir(), "shell")
				seed.GroupPath = DefaultGroupPath
				seed.tmuxSession = tmux.ReconnectSessionLazy("agentdeck_identity_old", seed.ID, seed.ProjectPath, "shell", "running")
				for _, s := range []*Storage{other, owner} {
					if err := s.SaveWithGroups([]*Instance{seed}, NewGroupTree([]*Instance{seed})); err != nil {
						t.Fatalf("seed save: %v", err)
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
				if ownerClosed {
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

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
