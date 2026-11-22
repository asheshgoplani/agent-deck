package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A Pi deck session with several session files and no saved identity has
// no single live document. The lookup asks the index next, and when the
// index has nothing either it answers why, not "no session matches".
func TestRowsLookupPiWithoutOneDocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[recall]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "project")
	inst := &session.Instance{ID: "pi-two-docs", Tool: "pi", Title: "pi two", ProjectPath: project}
	dir := filepath.Join(home, ".pi", "agent-deck", inst.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pi-a", "pi-b"} {
		header := `{"type":"session","version":3,"id":"` + id + `","cwd":"` + project + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(header), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lk := &rowsLookup{profile: "_test", instances: []*session.Instance{inst}, inBatch: true}
	_, err = lk.resolve(inst.ID)
	if err == nil || !strings.Contains(err.Error(), "ambiguous Pi context artifacts") {
		t.Fatalf("err = %v, want the ambiguous Pi documents reason", err)
	}

	// A single-id lookup whose index cannot be opened still answers with
	// the Pi reason instead of exiting on the index error.
	dbPath, err := recall.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	lk.close()
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dbPath, 0o700); err != nil {
		t.Fatal(err)
	}
	single := &rowsLookup{profile: "_test", instances: []*session.Instance{inst}}
	defer single.close()
	_, err = single.resolve(inst.ID)
	if err == nil || !strings.Contains(err.Error(), "ambiguous Pi context artifacts") {
		t.Fatalf("single lookup err = %v, want the ambiguous Pi documents reason", err)
	}
}
