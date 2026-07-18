package tree

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

func sessions(n int) []model.Session {
	s := make([]model.Session, n)
	for i := range s {
		s[i] = model.Session{ID: string(rune('a' + i))}
	}
	return s
}

func TestFlattenCollapsedShowsOnlyTopNode(t *testing.T) {
	// /w/a collapses to a single "w/a" project node.
	root := BuildTree([]model.Project{{Path: "/w/a", Name: "a", Sessions: sessions(3)}})
	rows := FlattenVisible(root, map[string]bool{}, map[string]int{}, 15)
	if len(rows) != 1 || rows[0].Kind != ProjectRow || rows[0].Node.Name != "w/a" {
		t.Fatalf("collapsed rows = %+v", rows)
	}
}

func TestFlattenHybridProjectWithSubProject(t *testing.T) {
	// /a is a project AND the parent of /a/b — both must be reachable.
	root := BuildTree([]model.Project{
		{Path: "/a", Name: "a", Sessions: sessions(2)},
		{Path: "/a/b", Name: "b", Sessions: sessions(3)},
	})
	rows := FlattenVisible(root, map[string]bool{"/a": true, "/a/b": true}, map[string]int{}, 15)
	var projects, sess int
	for _, r := range rows {
		if r.Kind == ProjectRow {
			projects++
		}
		if r.Kind == SessionRow {
			sess++
		}
	}
	if projects != 2 || sess != 5 {
		t.Fatalf("hybrid: projects=%d sessions=%d, want 2/5\nrows=%+v", projects, sess, rows)
	}
}

func TestFlattenPagesSessionsWithLoadMore(t *testing.T) {
	root := BuildTree([]model.Project{{Path: "/a", Name: "a", Sessions: sessions(20)}})
	expanded := map[string]bool{"/a": true}
	rows := FlattenVisible(root, expanded, map[string]int{}, 15)
	// 1 project row + 15 session rows + 1 load-more row
	var s, more int
	for _, r := range rows {
		switch r.Kind {
		case SessionRow:
			s++
		case LoadMoreRow:
			more++
			if r.Remaining != 5 {
				t.Errorf("remaining = %d, want 5", r.Remaining)
			}
		}
	}
	if s != 15 || more != 1 {
		t.Fatalf("sessions=%d loadmore=%d", s, more)
	}
}

func TestFlattenLoadedExpandsPage(t *testing.T) {
	root := BuildTree([]model.Project{{Path: "/a", Name: "a", Sessions: sessions(20)}})
	rows := FlattenVisible(root, map[string]bool{"/a": true}, map[string]int{"/a": 30}, 15)
	var s, more int
	for _, r := range rows {
		if r.Kind == SessionRow {
			s++
		}
		if r.Kind == LoadMoreRow {
			more++
		}
	}
	if s != 20 || more != 0 {
		t.Fatalf("sessions=%d loadmore=%d, want 20/0", s, more)
	}
}
