package tree

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

func TestScopeFilterKeepsRootAndDescendants(t *testing.T) {
	in := []model.Project{
		{Path: "/work/app"},
		{Path: "/work/app/sub"},
		{Path: "/work/other"},
		{Path: "/elsewhere"},
	}
	out := ScopeFilter(in, "/work/app")
	if len(out) != 2 {
		t.Fatalf("got %d projects, want 2 (app + app/sub): %+v", len(out), out)
	}
}

func TestScopeFilterEmptyRootReturnsAll(t *testing.T) {
	in := []model.Project{{Path: "/a"}, {Path: "/b"}}
	if out := ScopeFilter(in, ""); len(out) != 2 {
		t.Fatalf("empty root should pass all, got %d", len(out))
	}
}

func TestNodePathsToDepthExpandsTopOnly(t *testing.T) {
	// /w/a and /w/b → root child "w" (depth 0) with children a,b (depth 1).
	root := BuildTree([]model.Project{{Path: "/w/a"}, {Path: "/w/b"}})
	d1 := NodePathsToDepth(root, 1)
	if len(d1) != 1 || d1[0] != "/w" {
		t.Fatalf("depth 1 should expand only /w, got %v", d1)
	}
	d2 := NodePathsToDepth(root, 2)
	if len(d2) != 3 { // /w, /w/a, /w/b
		t.Fatalf("depth 2 should expand /w and its children, got %v", d2)
	}
}

func TestNodePathsCollectsFoldersAndProjects(t *testing.T) {
	root := BuildTree([]model.Project{
		{Path: "/w/a", Sessions: []model.Session{{ID: "1"}}},
		{Path: "/w/b"},
	})
	found := map[string]bool{}
	for _, p := range NodePaths(root) {
		found[p] = true
	}
	if !found["/w/a"] || !found["/w/b"] {
		t.Fatalf("NodePaths missing project paths: %v", NodePaths(root))
	}
}
