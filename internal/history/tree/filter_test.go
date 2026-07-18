package tree

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

func TestFilterKeepsMatchingSessions(t *testing.T) {
	projects := []model.Project{{
		Path: "/a", Name: "a",
		Sessions: []model.Session{
			{ID: "1", Title: "Fix S3 cache", CWD: "/a"},
			{ID: "2", Title: "Add login", CWD: "/a", GitBranch: "feature/auth"},
		},
	}}
	out := Filter(projects, "auth")
	if len(out) != 1 || len(out[0].Sessions) != 1 || out[0].Sessions[0].ID != "2" {
		t.Fatalf("filtered = %+v", out)
	}
}

func TestFilterMatchesMultibyteQuery(t *testing.T) {
	projects := []model.Project{{
		Path: "/a",
		Sessions: []model.Session{
			{ID: "1", Title: "café menu redesign"},
			{ID: "2", Title: "plain ascii task"},
		},
	}}
	out := Filter(projects, "café")
	if len(out) != 1 || len(out[0].Sessions) != 1 || out[0].Sessions[0].ID != "1" {
		t.Fatalf("multibyte filter = %+v", out)
	}
}

func TestFilterEmptyQueryReturnsAll(t *testing.T) {
	in := []model.Project{{Path: "/a", Sessions: []model.Session{{ID: "1"}}}}
	if out := Filter(in, ""); len(out) != 1 || len(out[0].Sessions) != 1 {
		t.Fatalf("empty query changed input: %+v", out)
	}
}
