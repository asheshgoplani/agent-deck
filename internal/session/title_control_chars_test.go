package session

import (
	"testing"
	"time"
)

// A title is printed by `agent-deck list` and rendered in the TUI. One that
// carries an escape sequence (stored by an older `openclaw sync`, or typed into
// a web request) must come back from storage and from the constructor without
// control or bidi characters, so it cannot drive the terminal on every open.
const hostileTitle = "agent\x1b]52;c;aGk=\x07\r\n‮evil"

func TestStorageLoad_CleansControlCharactersInTitle(t *testing.T) {
	s := newTestStorage(t)
	inst := &Instance{
		ID: "title-ctl-1", Title: hostileTitle, ProjectPath: "/tmp/test",
		GroupPath: "g", Tool: "shell", Status: StatusIdle, CreatedAt: time.Now(),
	}
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	lite, _, err := s.LoadLite()
	if err != nil {
		t.Fatalf("LoadLite: %v", err)
	}
	if len(loaded) != 1 || len(lite) != 1 {
		t.Fatalf("loaded %d / lite %d instances, want 1", len(loaded), len(lite))
	}
	const want = "agent]52;c;aGk=evil"
	if loaded[0].Title != want {
		t.Errorf("LoadWithGroups title = %q, want %q", loaded[0].Title, want)
	}
	if lite[0].Title != want {
		t.Errorf("LoadLite title = %q, want %q", lite[0].Title, want)
	}
}

func TestNewInstanceWithTool_CleansControlCharactersInTitle(t *testing.T) {
	inst := NewInstanceWithTool(hostileTitle, t.TempDir(), "shell")
	if want := "agent]52;c;aGk=evil"; inst.Title != want {
		t.Fatalf("title = %q, want %q", inst.Title, want)
	}
}

// Renames (CLI, web, TUI) and Claude title sync go through SetTitleThreadSafe;
// the TUI draws the in-memory title, so it must be cleaned there too.
func TestSetTitleThreadSafe_CleansControlCharacters(t *testing.T) {
	inst := &Instance{Title: "old"}
	inst.SetTitleThreadSafe(hostileTitle)
	if want := "agent]52;c;aGk=evil"; inst.Title != want {
		t.Fatalf("title = %q, want %q", inst.Title, want)
	}
}

// A remote host's `list --json` titles and groups are rendered in the TUI.
func TestParseRemoteSessions_CleansTitleAndGroup(t *testing.T) {
	rows, err := parseRemoteSessions([]byte(`[{"id":"a","title":"t\u001b]52;c;aGk=\u0007x","group":"g\u001b[2Jy","tool":"claude","status":"idle"}]`))
	if err != nil || len(rows) != 1 {
		t.Fatalf("parse: rows=%d err=%v", len(rows), err)
	}
	if rows[0].Title != "t]52;c;aGk=x" || rows[0].Group != "g[2Jy" {
		t.Fatalf("title=%q group=%q", rows[0].Title, rows[0].Group)
	}
}
