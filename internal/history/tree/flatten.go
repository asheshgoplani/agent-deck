package tree

import "github.com/asheshgoplani/agent-deck/internal/history/model"

type RowKind int

const (
	FolderRow RowKind = iota
	ProjectRow
	SessionRow
	LoadMoreRow
)

type Row struct {
	Kind      RowKind
	Depth     int
	Node      *Node
	Session   *model.Session
	Path      string
	Remaining int
}

func FlattenVisible(root *Node, expanded map[string]bool, loaded map[string]int, pageSize int) []Row {
	var rows []Row
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		for _, c := range n.Children {
			kind := FolderRow
			if c.Project != nil {
				kind = ProjectRow
			}
			rows = append(rows, Row{Kind: kind, Depth: depth, Node: c, Path: c.Path})
			if !expanded[c.Path] {
				continue
			}
			// A node can be both a project and the parent of sub-projects
			// (e.g. /madp and /madp/web-app). Show sub-folders first, then
			// this node's own sessions.
			if len(c.Children) > 0 {
				walk(c, depth+1)
			}
			if c.Project != nil {
				emitSessions(&rows, c.Project, depth+1, loaded, pageSize)
			}
		}
	}
	walk(root, 0)
	return rows
}

func emitSessions(rows *[]Row, p *model.Project, depth int, loaded map[string]int, pageSize int) {
	limit := pageSize
	if l := loaded[p.Path]; l > limit {
		limit = l
	}
	if limit > len(p.Sessions) {
		limit = len(p.Sessions)
	}
	for i := 0; i < limit; i++ {
		*rows = append(*rows, Row{Kind: SessionRow, Depth: depth, Session: &p.Sessions[i], Path: p.Path})
	}
	if rem := len(p.Sessions) - limit; rem > 0 {
		*rows = append(*rows, Row{Kind: LoadMoreRow, Depth: depth, Path: p.Path, Remaining: rem})
	}
}
