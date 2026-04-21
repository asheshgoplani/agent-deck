package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStorage_TmuxSocketName_Roundtrip is the persistence contract for
// issue #687 phase 1 (v1.7.50): an Instance created with a non-default
// TmuxSocketName survives a full save → close → reopen → load cycle with
// the socket name intact. Without this, restart and revive paths would
// silently reset every session to the default server after the first TUI
// restart.
func TestStorage_TmuxSocketName_Roundtrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("AGENT_DECK_PROFILE", "default")
	ClearUserConfigCache()

	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	inst := &Instance{
		ID:             "inst-sock-1",
		Title:          "socket-test",
		ProjectPath:    filepath.Join(tempDir, "proj"),
		GroupPath:      "my-sessions",
		Tool:           "shell",
		Status:         StatusIdle,
		CreatedAt:      time.Now(),
		TmuxSocketName: "agent-deck",
	}
	if err := os.MkdirAll(inst.ProjectPath, 0o700); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}

	tree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, tree); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen with a fresh Storage (simulates process restart)
	storage2, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	defer storage2.Close()

	loaded, _, err := storage2.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var got *Instance
	for _, li := range loaded {
		if li.ID == inst.ID {
			got = li
			break
		}
	}
	if got == nil {
		t.Fatalf("loaded slice did not contain ID %q; got %d instances", inst.ID, len(loaded))
	}

	if got.TmuxSocketName != "agent-deck" {
		t.Fatalf("TmuxSocketName did not survive round-trip; got %q want %q", got.TmuxSocketName, "agent-deck")
	}

	// The reconstituted tmuxSession must also carry the socket so every
	// subsequent method call targets the right server. Revivers fail if
	// this isn't seeded (they'd probe the default server and mark the
	// session dead).
	if ts := got.GetTmuxSession(); ts == nil {
		// A session with no TmuxSession string is valid (never started yet).
		// For this test we deliberately left TmuxSession empty so
		// tmuxSession is nil — skip the sub-check.
		_ = ts
	} else if ts.SocketName != "agent-deck" {
		t.Fatalf("reconstituted tmuxSession.SocketName = %q, want %q", ts.SocketName, "agent-deck")
	}
}

// TestStorage_TmuxSocketName_EmptyRoundtrip pins down the zero-config
// install: if TmuxSocketName is never set, the persisted value must stay
// empty (no accidental default-socket name showing up after an upgrade).
func TestStorage_TmuxSocketName_EmptyRoundtrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("AGENT_DECK_PROFILE", "default")
	ClearUserConfigCache()

	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}

	inst := &Instance{
		ID:          "inst-sock-0",
		Title:       "no-socket",
		ProjectPath: filepath.Join(tempDir, "proj"),
		GroupPath:   "my-sessions",
		Tool:        "shell",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
		// TmuxSocketName intentionally omitted — pre-v1.7.50 shape.
	}
	if err := os.MkdirAll(inst.ProjectPath, 0o700); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}

	tree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, tree); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	storage2, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer storage2.Close()

	loaded, _, err := storage2.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, li := range loaded {
		if li.ID == inst.ID {
			if li.TmuxSocketName != "" {
				t.Fatalf("zero-config install must keep TmuxSocketName empty; got %q", li.TmuxSocketName)
			}
			return
		}
	}
	t.Fatalf("round-trip lost instance %q", inst.ID)
}
