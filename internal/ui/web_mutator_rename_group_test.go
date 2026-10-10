// WebMutator.RenameGroup reports where the group lives after the rename.
// The browser needs it (issue #2555): a rename rewrites the last path segment
// from the new name, so a group selected in the web UI under its old path
// would otherwise point at a group that no longer exists.

package ui

import "testing"

func TestWebMutatorRenameGroupReportsNewPath(t *testing.T) {
	home := homeWithGroups(t, "work", "work/innotrade")
	m := &WebMutator{h: home}

	got, err := m.RenameGroup("work/innotrade", "Inno Trade")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got == "work/innotrade" {
		t.Fatalf("rename reported the old path %q; expected the moved path", got)
	}
	if _, ok := home.groupTree.Groups[got]; !ok {
		t.Fatalf("reported path %q is not a group in the tree", got)
	}
	if _, ok := home.groupTree.Groups["work/innotrade"]; ok {
		t.Fatal("old path still present after rename")
	}
}

func TestWebMutatorRenameGroupSamePathReportsIt(t *testing.T) {
	home := homeWithGroups(t, "work")
	m := &WebMutator{h: home}

	got, err := m.RenameGroup("work", "work")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got != "work" {
		t.Fatalf("rename to the same name reported %q, want %q", got, "work")
	}
}
