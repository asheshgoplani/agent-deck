package ui

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/menudata"
)

// TestWebCreateGroup_UnknownParentRejected: POST /api/groups with a parentPath
// that does not exist must not store an orphan "nope/x" group. The CLI refuses
// the same request with "parent group not found".
func TestWebCreateGroup_UnknownParentRejected(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_webgrp_orphan")
	_ = seedSession(t, storage, nil, "webgrp-orphan-001", "s1")
	m := NewWebMutator(h)

	if _, err := m.CreateGroup("work", ""); err != nil {
		t.Fatalf("create work: %v", err)
	}

	_, err := m.CreateGroup("x", "nope")
	if !errors.Is(err, menudata.ErrGroupNotFound) {
		t.Fatalf("CreateGroup with unknown parent: want ErrGroupNotFound, got %v", err)
	}
	if _, ok := h.groupTree.Groups["nope/x"]; ok {
		t.Fatal("orphan group nope/x created in memory")
	}
	_, groups, lerr := storage.LoadWithGroups()
	if lerr != nil {
		t.Fatalf("LoadWithGroups: %v", lerr)
	}
	for _, g := range groups {
		if g.Path == "nope/x" {
			t.Fatal("orphan group nope/x persisted")
		}
	}

	p, err := m.CreateGroup("x", "work")
	if err != nil || p != "work/x" {
		t.Fatalf("valid subgroup: path=%q err=%v", p, err)
	}
}
