package tree

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

func TestBuildTreeNestsSharedParents(t *testing.T) {
	projects := []model.Project{
		{Path: "/work/firegroup/web", Name: "web"},
		{Path: "/work/firegroup/api", Name: "api"},
		{Path: "/work/other", Name: "other"},
	}
	root := BuildTree(projects)
	// root -> "work" -> {"firegroup" -> {web, api}, other}
	work := findChild(root, "work")
	if work == nil {
		t.Fatal("missing /work node")
	}
	fg := findChild(work, "firegroup")
	if fg == nil || len(fg.Children) != 2 {
		t.Fatalf("firegroup children = %v", fg)
	}
	other := findChild(work, "other")
	if other == nil || other.Project == nil {
		t.Fatal("other should be a project leaf")
	}
}

func TestBuildTreeCollapsesSingleChildChains(t *testing.T) {
	root := BuildTree([]model.Project{{Path: "/x/y/z/proj", Name: "proj"}})
	if len(root.Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(root.Children))
	}
	c := root.Children[0]
	if c.Name != "x/y/z/proj" || c.Project == nil {
		t.Fatalf("collapsed node = %q (project=%v)", c.Name, c.Project != nil)
	}
	if c.Path != "/x/y/z/proj" {
		t.Errorf("collapsed node path = %q, want project path", c.Path)
	}
}

func findChild(n *Node, name string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}
