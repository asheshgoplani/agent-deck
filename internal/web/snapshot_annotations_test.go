package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestApplySnapshotAnnotations_FillsMatchingSessions(t *testing.T) {
	t.Parallel()
	snap := snapshotWithSession("sess-A", "claude", session.StatusRunning)
	var gotProfile string
	applySnapshotAnnotations(snap, func(profile string) (map[string]*statedb.InstanceAnnotations, error) {
		gotProfile = profile
		return map[string]*statedb.InstanceAnnotations{
			"sess-A": {Hints: map[string]string{"headline": "ship it", "status": "needs-input"}, Tags: []string{"web"}},
			"other":  {Hints: map[string]string{"headline": "not in snapshot"}},
		}, nil
	})
	if gotProfile != "test" {
		t.Fatalf("loader profile = %q; want the snapshot's profile", gotProfile)
	}
	s := snap.Items[0].Session
	if s.Hints["headline"] != "ship it" || s.Hints["status"] != "needs-input" || len(s.Tags) != 1 || s.Tags[0] != "web" {
		t.Fatalf("session annotations = hints %v tags %v", s.Hints, s.Tags)
	}
}

func TestApplySnapshotAnnotations_LoadErrorLeavesSnapshotUsable(t *testing.T) {
	t.Parallel()
	snap := snapshotWithSession("sess-A", "claude", session.StatusIdle)
	applySnapshotAnnotations(snap, func(string) (map[string]*statedb.InstanceAnnotations, error) {
		return nil, errors.New("no state.db")
	})
	if s := snap.Items[0].Session; s.Hints != nil || s.Tags != nil {
		t.Fatalf("a failed load must not invent annotations: %+v", s)
	}
	applySnapshotAnnotations(snap, nil)
	applySnapshotAnnotations(nil, nil)
}

// The menu endpoint the web UI reads must carry hints/tags on the wire.
func TestHandleMenu_SerializesAnnotations(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	fx.store.mu.Lock()
	id := fx.store.order[0]
	fx.store.mu.Unlock()
	fx.server.annotationLoader = func(string) (map[string]*statedb.InstanceAnnotations, error) {
		return map[string]*statedb.InstanceAnnotations{
			id: {Hints: map[string]string{"headline": "expose hints", "ticket": "ENG-1"}, Tags: []string{"tooling"}},
		}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	w := httptest.NewRecorder()
	fx.server.handleMenu(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/menu: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			Session *struct {
				ID    string            `json:"id"`
				Hints map[string]string `json:"hints"`
				Tags  []string          `json:"tags"`
			} `json:"session"`
		} `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, item := range resp.Items {
		if item.Session == nil {
			continue
		}
		if item.Session.ID == id {
			seen++
			if item.Session.Hints["headline"] != "expose hints" || item.Session.Hints["ticket"] != "ENG-1" ||
				len(item.Session.Tags) != 1 || item.Session.Tags[0] != "tooling" {
				t.Fatalf("annotated session on the wire = %+v", item.Session)
			}
		} else if item.Session.Hints != nil || item.Session.Tags != nil {
			t.Fatalf("unannotated session %s grew annotations: %+v", item.Session.ID, item.Session)
		}
	}
	if seen != 1 {
		t.Fatalf("annotated session %s seen %d times", id, seen)
	}
}

// The production reader opens the profile's real state.db read-only and sees
// what `session annotate` wrote through a separate read-write handle.
func TestStateDBAnnotationReader_ReadsProfileStateDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := session.GetDBPathForProfile("annot-test")
	if err != nil {
		t.Fatal(err)
	}
	rw, err := statedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if err := rw.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := rw.SetSessionHint(statedb.HintScopeInstance, "inst-1", "status", "ready-for-review", statedb.HintSourceAnnotate, ""); err != nil {
		t.Fatal(err)
	}

	r := &stateDBAnnotationReader{}
	defer r.close()
	got, err := r.load("annot-test")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["inst-1"] == nil || got["inst-1"].Hints["status"] != "ready-for-review" {
		t.Fatalf("annotations = %+v", got)
	}

	// A later write is visible on the cached handle without reopening.
	if err := rw.SetSessionHint(statedb.HintScopeInstance, "inst-1", "status", "done", statedb.HintSourceAnnotate, ""); err != nil {
		t.Fatal(err)
	}
	got, err = r.load("annot-test")
	if err != nil || got["inst-1"].Hints["status"] != "done" {
		t.Fatalf("after update: %+v, %v", got, err)
	}
}
