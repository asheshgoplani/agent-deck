package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestChildrenOfFiltersByParent(t *testing.T) {
	a := &session.Instance{ID: "p"}
	b := &session.Instance{ID: "c1", ParentSessionID: "p"}
	c := &session.Instance{ID: "c2", ParentSessionID: "other"}
	d := &session.Instance{ID: "c3", ParentSessionID: "p"}
	got := childrenOf("p", []*session.Instance{a, b, c, d})
	if len(got) != 2 {
		t.Fatalf("expected 2 children, got %d", len(got))
	}
	if got[0].ID != "c1" || got[1].ID != "c3" {
		t.Fatalf("unexpected children: %v %v", got[0].ID, got[1].ID)
	}
}
