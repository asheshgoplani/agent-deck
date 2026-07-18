package tree

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

type Node struct {
	Name     string
	Path     string
	Children []*Node
	Project  *model.Project
}

func (n *Node) child(name, path string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	c := &Node{Name: name, Path: path}
	n.Children = append(n.Children, c)
	return c
}

// BuildTree groups projects into a nested folder tree by path segments,
// then collapses single-child folder chains (e.g. /private/var/www/html →
// one node) so deep paths stay navigable.
func BuildTree(projects []model.Project) *Node {
	root := &Node{}
	for i := range projects {
		p := projects[i]
		segs := strings.Split(strings.Trim(p.Path, "/"), "/")
		cur := root
		path := ""
		for j, seg := range segs {
			path += "/" + seg
			cur = cur.child(seg, path)
			if j == len(segs)-1 {
				cur.Project = &projects[i]
			}
		}
	}
	collapse(root)
	return root
}

// collapse merges any folder node (no project of its own) that has exactly
// one child into that child, joining their names with "/". A node that is
// itself a project is never merged away, so project sessions stay reachable.
func collapse(n *Node) {
	for _, c := range n.Children {
		collapse(c)
	}
	for i := range n.Children {
		c := n.Children[i]
		for c.Project == nil && len(c.Children) == 1 {
			only := c.Children[0]
			only.Name = c.Name + "/" + only.Name
			n.Children[i] = only
			c = only
		}
	}
}
