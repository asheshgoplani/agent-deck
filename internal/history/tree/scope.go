package tree

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

// ScopeFilter keeps projects whose path is root or a descendant of root.
// An empty root returns everything (global view).
func ScopeFilter(projects []model.Project, root string) []model.Project {
	if root == "" {
		return projects
	}
	root = strings.TrimRight(root, "/")
	var out []model.Project
	for _, p := range projects {
		if p.Path == root || strings.HasPrefix(p.Path, root+"/") {
			out = append(out, p)
		}
	}
	return out
}

// NodePaths returns the paths of every folder and project node in the tree,
// used to auto-expand the whole tree on launch.
func NodePaths(root *Node) []string {
	var out []string
	var walk func(n *Node)
	walk = func(n *Node) {
		for _, c := range n.Children {
			out = append(out, c.Path)
			walk(c)
		}
	}
	walk(root)
	return out
}

// NodePathsToDepth returns node paths to expand so the tree shows down to
// maxDepth levels: nodes at depth < maxDepth get expanded (depth 0 = the
// top-level children of root).
func NodePathsToDepth(root *Node, maxDepth int) []string {
	var out []string
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		for _, c := range n.Children {
			if depth < maxDepth {
				out = append(out, c.Path)
			}
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	return out
}
