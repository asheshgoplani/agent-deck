package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `ls --json` used to carry no parent field at all. A conductor verifying that
// a child had parented read `.parent_id` and got null — indistinguishable from
// "this session has no parent" — so the check silently passed for the wrong
// reason, and kept passing after `session set-parent` reported success.
//
// The key is therefore emitted UNCONDITIONALLY: "" means genuinely unparented,
// and a missing key means the binary predates this fix. omitempty would
// collapse those two back into the same null that caused the bug.

func setupListParentIDTest(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Chdir(home)

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	return "list_parent_id"
}

func captureListStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String()
}

func TestListJSON_ExposesParentID(t *testing.T) {
	profile := setupListParentIDTest(t)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	instances := []*session.Instance{
		{ID: "conductor-1", Title: "conductor", ProjectPath: t.TempDir()},
		{ID: "child-1", Title: "impl-thing", ProjectPath: t.TempDir(), ParentSessionID: "conductor-1"},
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTree(nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	out := captureListStdout(t, func() { handleList(profile, []string{"--json"}) })

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal ls --json output: %v\noutput was:\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 sessions, got %d: %s", len(rows), out)
	}

	byID := map[string]map[string]json.RawMessage{}
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("unmarshal id: %v", err)
		}
		byID[id] = row
	}

	for _, id := range []string{"conductor-1", "child-1"} {
		if _, ok := byID[id]["parent_id"]; !ok {
			t.Errorf("session %s: ls --json omits parent_id entirely; "+
				"a consumer cannot tell 'unparented' from 'this view has no answer'", id)
		}
	}

	var childParent string
	if err := json.Unmarshal(byID["child-1"]["parent_id"], &childParent); err != nil {
		t.Fatalf("unmarshal child parent_id: %v", err)
	}
	if childParent != "conductor-1" {
		t.Errorf("child-1 parent_id = %q, want %q", childParent, "conductor-1")
	}

	var conductorParent string
	if err := json.Unmarshal(byID["conductor-1"]["parent_id"], &conductorParent); err != nil {
		t.Fatalf("unmarshal conductor parent_id: %v", err)
	}
	if conductorParent != "" {
		t.Errorf("conductor-1 parent_id = %q, want \"\" (unparented)", conductorParent)
	}
}

func TestListAllProfilesJSON_ExposesParentID(t *testing.T) {
	profile := setupListParentIDTest(t)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	instances := []*session.Instance{
		{ID: "child-2", Title: "impl-other", ProjectPath: t.TempDir(), ParentSessionID: "conductor-9"},
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTree(nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	out := captureListStdout(t, func() { handleListAllProfiles(true) })

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal ls --all --json output: %v\noutput was:\n%s", err, out)
	}

	var found bool
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			continue
		}
		if id != "child-2" {
			continue
		}
		found = true
		raw, ok := row["parent_id"]
		if !ok {
			t.Fatal("ls --all --json omits parent_id entirely")
		}
		var parent string
		if err := json.Unmarshal(raw, &parent); err != nil {
			t.Fatalf("unmarshal parent_id: %v", err)
		}
		if parent != "conductor-9" {
			t.Errorf("child-2 parent_id = %q, want %q", parent, "conductor-9")
		}
	}
	if !found {
		t.Fatalf("seeded session not present in ls --all --json output:\n%s", out)
	}
}
