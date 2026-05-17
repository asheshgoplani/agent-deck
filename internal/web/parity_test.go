package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestParity_WebActionMatchesDirectMutator covers the runtime sync invariant
// that the WebUI overhaul plan (documentation/webui-overhaul-plan.md) lists
// as failure mode #2 — "state drift: web and TUI disagree about session
// status, group membership."
//
// For each lifecycle action exposed by the web HTTP API, we fire the action
// twice in parallel test cases:
//  1. Through the HTTP handler (the path the WebUI takes).
//  2. Directly against the SessionMutator interface (the path the TUI's
//     WebMutator uses internally).
//
// We then compare the resulting MenuSnapshot. Both paths must produce the
// same observable state. If the two layers ever diverge (e.g. an HTTP
// handler grows side-effects the mutator doesn't, or vice versa), this
// test fails and the matrix is flagged.
func TestParity_WebActionMatchesDirectMutator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// fire the web HTTP path and direct mutator path against fresh stores;
		// returns the changed-session id so the assertion can compare just
		// that record.
		fire func(t *testing.T, web *parityFixture, direct *parityFixture) string
	}{
		{
			name: "create_session",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				body, _ := json.Marshal(map[string]string{
					"title":       "parity-create",
					"tool":        "claude",
					"projectPath": "/srv/parity",
					"groupPath":   "work",
				})
				req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				webFx.server.handleSessionsCollection(w, req)
				if w.Code != http.StatusCreated {
					t.Fatalf("web POST /api/sessions: status=%d body=%s", w.Code, w.Body.String())
				}
				var resp SessionActionResponse
				_ = json.NewDecoder(w.Body).Decode(&resp)

				// Direct mutator path.
				_, err := directFx.store.CreateSession("parity-create", "claude", "/srv/parity", "work", "")
				if err != nil {
					t.Fatalf("direct CreateSession: %v", err)
				}
				return resp.SessionID
			},
		},
		{
			name: "stop_session",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				_, _ = webFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				_, _ = directFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				// Both stores generated the same id deterministically (sess-005).
				const id = "sess-005"

				req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/stop", nil)
				w := httptest.NewRecorder()
				webFx.server.handleSessionByAction(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("web stop: status=%d body=%s", w.Code, w.Body.String())
				}

				if err := directFx.store.StopSession(id); err != nil {
					t.Fatalf("direct StopSession: %v", err)
				}
				return id
			},
		},
		{
			name: "start_session",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				_, _ = webFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				_, _ = directFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				const id = "sess-005"

				req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/start", nil)
				w := httptest.NewRecorder()
				webFx.server.handleSessionByAction(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("web start: status=%d", w.Code)
				}
				if err := directFx.store.StartSession(id); err != nil {
					t.Fatalf("direct StartSession: %v", err)
				}
				return id
			},
		},
		{
			name: "delete_session",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				_, _ = webFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				_, _ = directFx.store.CreateSession("seed", "claude", "/srv/seed", "work", "")
				const id = "sess-005"

				req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+id, nil)
				w := httptest.NewRecorder()
				webFx.server.handleSessionByAction(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("web delete: status=%d", w.Code)
				}
				if err := directFx.store.DeleteSession(id); err != nil {
					t.Fatalf("direct DeleteSession: %v", err)
				}
				return id
			},
		},
		{
			// Web POST /api/groups vs direct CreateGroup mutator. Asserts both
			// paths produce a snapshot containing the new group.
			name: "create_group",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				body, _ := json.Marshal(map[string]string{
					"name":       "experiments",
					"parentPath": "",
				})
				req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				webFx.server.handleGroupsCollection(w, req)
				if w.Code != http.StatusCreated {
					t.Fatalf("web POST /api/groups: status=%d body=%s", w.Code, w.Body.String())
				}
				if _, err := directFx.store.CreateGroup("experiments", ""); err != nil {
					t.Fatalf("direct CreateGroup: %v", err)
				}
				return "group:experiments"
			},
		},
		{
			name: "rename_group",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				body, _ := json.Marshal(map[string]string{"name": "home"})
				req := httptest.NewRequest(http.MethodPatch, "/api/groups/work", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				webFx.server.handleGroupByPath(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("web PATCH /api/groups/work: status=%d body=%s", w.Code, w.Body.String())
				}
				if err := directFx.store.RenameGroup("work", "home"); err != nil {
					t.Fatalf("direct RenameGroup: %v", err)
				}
				return "group:work"
			},
		},
		{
			name: "delete_group",
			fire: func(t *testing.T, webFx, directFx *parityFixture) string {
				// Seed an extra group so deleting one doesn't leave the
				// snapshot empty (and isn't the default group).
				_, _ = webFx.store.CreateGroup("scratch", "")
				_, _ = directFx.store.CreateGroup("scratch", "")

				req := httptest.NewRequest(http.MethodDelete, "/api/groups/scratch", nil)
				w := httptest.NewRecorder()
				webFx.server.handleGroupByPath(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("web DELETE /api/groups/scratch: status=%d body=%s", w.Code, w.Body.String())
				}
				if err := directFx.store.DeleteGroup("scratch"); err != nil {
					t.Fatalf("direct DeleteGroup: %v", err)
				}
				return "group:scratch"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			webFx := newParityFixture()
			directFx := newParityFixture()

			id := tc.fire(t, webFx, directFx)

			webSnap, err := webFx.store.LoadMenuSnapshot()
			if err != nil {
				t.Fatalf("web snapshot: %v", err)
			}
			directSnap, err := directFx.store.LoadMenuSnapshot()
			if err != nil {
				t.Fatalf("direct snapshot: %v", err)
			}

			// Group-typed cases: id is "group:<path>".
			if path, ok := strings.CutPrefix(id, "group:"); ok {
				webGrp := findGroupByPath(webSnap, path)
				directGrp := findGroupByPath(directSnap, path)
				if tc.name == "delete_group" {
					if webGrp != nil || directGrp != nil {
						t.Fatalf("delete_group: expected absent in both, got web=%+v direct=%+v", webGrp, directGrp)
					}
				} else {
					if webGrp == nil || directGrp == nil {
						t.Fatalf("post-action group missing: web=%v direct=%v (path=%q)", webGrp != nil, directGrp != nil, path)
					}
					if webGrp.Name != directGrp.Name {
						t.Fatalf("group name drift: web=%q direct=%q (path=%q)", webGrp.Name, directGrp.Name, path)
					}
				}
				if webSnap.TotalGroups != directSnap.TotalGroups {
					t.Fatalf("group count drift: web=%d direct=%d", webSnap.TotalGroups, directSnap.TotalGroups)
				}
				return
			}

			webSess := findSessionByID(webSnap, id)
			directSess := findSessionByID(directSnap, id)

			// For delete the session should be absent in both snapshots.
			if tc.name == "delete_session" {
				if webSess != nil || directSess != nil {
					t.Fatalf("delete: expected absent in both, got web=%+v direct=%+v", webSess, directSess)
				}
				return
			}

			if webSess == nil || directSess == nil {
				t.Fatalf("post-action session missing: web=%v direct=%v (id=%q)", webSess != nil, directSess != nil, id)
			}
			if webSess.Status != directSess.Status {
				t.Fatalf("status drift: web=%q direct=%q (id=%q)", webSess.Status, directSess.Status, id)
			}
			if webSess.Title != directSess.Title {
				t.Fatalf("title drift: web=%q direct=%q", webSess.Title, directSess.Title)
			}
			if webSess.GroupPath != directSess.GroupPath {
				t.Fatalf("group drift: web=%q direct=%q", webSess.GroupPath, directSess.GroupPath)
			}
			if webSess.Tool != directSess.Tool {
				t.Fatalf("tool drift: web=%q direct=%q", webSess.Tool, directSess.Tool)
			}
			if webSnap.TotalSessions != directSnap.TotalSessions {
				t.Fatalf("count drift: web=%d direct=%d", webSnap.TotalSessions, directSnap.TotalSessions)
			}
		})
	}
}

// TestParity_TUIChangeVisibleViaWebAPI exercises the inverse direction:
// when the canonical store transitions a session (simulating a TUI-side
// action), the next call to the web /api/sessions endpoint must reflect
// the new state with no caching layer in between.
func TestParity_TUIChangeVisibleViaWebAPI(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	_, _ = fx.store.CreateSession("ts", "claude", "/srv/ts", "work", "")
	const id = "sess-005"

	// "TUI" path: mutate the store directly.
	if err := fx.store.StopSession(id); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// "Web client" path: read /api/sessions.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionsCollection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d", w.Code)
	}
	var resp struct {
		Sessions []*MenuSession `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found *MenuSession
	for _, s := range resp.Sessions {
		if s.ID == id {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatalf("created session %q not visible to web /api/sessions", id)
	}
	if found.Status != session.StatusStopped {
		t.Fatalf("expected web to see stopped, saw %q", found.Status)
	}
}

// parityFixture wraps a minimal in-memory store + a Server that points at it.
// Lives here (rather than tests/web/fixtures/) because Go tests cannot import
// from fixture binaries; the implementations are intentionally simple
// duplicates of what tests/web/fixtures/cmd/web-fixture/main.go does.
type parityFixture struct {
	store  *parityStore
	server *Server
}

func newParityFixture() *parityFixture {
	store := newParityStore()
	store.seed()
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		Profile:      "parity-test",
		WebMutations: true,
		MenuData:     store,
	})
	srv.SetMutator(store)
	return &parityFixture{store: store, server: srv}
}

type parityStore struct {
	mu       sync.Mutex
	now      func() time.Time
	groups   map[string]*MenuGroup
	sessions map[string]*MenuSession
	order    []string
	nextID   int
}

func newParityStore() *parityStore {
	return &parityStore{
		now:      func() time.Time { return time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC) },
		groups:   make(map[string]*MenuGroup),
		sessions: make(map[string]*MenuSession),
	}
}

func (s *parityStore) seed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups["work"] = &MenuGroup{Name: "work", Path: "work", Order: 0}
	s.sessions["sess-001"] = &MenuSession{
		ID: "sess-001", Title: "agent-deck", Tool: "claude",
		Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/srv/agent-deck",
		Order: 0, CreatedAt: s.now(),
	}
	s.sessions["sess-002"] = &MenuSession{
		ID: "sess-002", Title: "frontend", Tool: "claude",
		Status: session.StatusRunning, GroupPath: "work", ProjectPath: "/srv/frontend",
		Order: 1, CreatedAt: s.now(),
	}
	s.sessions["sess-003"] = &MenuSession{
		ID: "sess-003", Title: "innotrade-api", Tool: "codex",
		Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/srv/innotrade-api",
		Order: 2, CreatedAt: s.now(),
	}
	s.sessions["sess-004"] = &MenuSession{
		ID: "sess-004", Title: "scratch", Tool: "shell",
		Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/home/dev/scratch",
		Order: 3, CreatedAt: s.now(),
	}
	s.order = []string{"sess-001", "sess-002", "sess-003", "sess-004"}
	s.nextID = 5
}

func (s *parityStore) LoadMenuSnapshot() (*MenuSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]MenuItem, 0, len(s.groups)+len(s.sessions))
	idx := 0
	for _, g := range s.groups {
		items = append(items, MenuItem{Index: idx, Type: MenuItemTypeGroup, Path: g.Path, Group: g})
		idx++
	}
	for _, id := range s.order {
		sess, ok := s.sessions[id]
		if !ok {
			continue
		}
		items = append(items, MenuItem{Index: idx, Type: MenuItemTypeSession, Session: sess})
		idx++
	}
	return &MenuSnapshot{
		Profile:       "parity-test",
		GeneratedAt:   s.now(),
		TotalGroups:   len(s.groups),
		TotalSessions: len(s.sessions),
		Items:         items,
	}, nil
}

// SessionMutator implementation.

func (s *parityStore) CreateSession(title, tool, projectPath, groupPath, modelID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := nextDeterministicID(&s.nextID)
	s.sessions[id] = &MenuSession{
		ID: id, Title: title, Tool: tool,
		Status: session.StatusIdle, GroupPath: groupPath, ProjectPath: projectPath,
		Order: len(s.order), CreatedAt: s.now(),
	}
	s.order = append(s.order, id)
	return id, nil
}

func (s *parityStore) StartSession(id string) error   { return s.transition(id, session.StatusRunning) }
func (s *parityStore) StopSession(id string) error    { return s.transition(id, session.StatusStopped) }
func (s *parityStore) RestartSession(id string) error { return s.transition(id, session.StatusRunning) }

func (s *parityStore) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return errNotFound(id)
	}
	delete(s.sessions, id)
	for i, x := range s.order {
		if x == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

func (s *parityStore) ForkSession(parentID string) (string, error) {
	s.mu.Lock()
	parent, ok := s.sessions[parentID]
	if !ok {
		s.mu.Unlock()
		return "", errNotFound(parentID)
	}
	id := nextDeterministicID(&s.nextID)
	s.sessions[id] = &MenuSession{
		ID: id, Title: parent.Title + " (fork)", Tool: parent.Tool,
		Status: session.StatusIdle, GroupPath: parent.GroupPath, ProjectPath: parent.ProjectPath,
		ParentSessionID: parentID, Order: len(s.order), CreatedAt: s.now(),
	}
	s.order = append(s.order, id)
	s.mu.Unlock()
	return id, nil
}

func (s *parityStore) CreateGroup(name, parentPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := name
	if parentPath != "" {
		path = parentPath + "/" + name
	}
	if _, exists := s.groups[path]; exists {
		return "", errAlreadyExists(path)
	}
	s.groups[path] = &MenuGroup{Name: name, Path: path, Order: len(s.groups)}
	return path, nil
}

func (s *parityStore) RenameGroup(groupPath, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupPath]
	if !ok {
		return errNotFound(groupPath)
	}
	g.Name = newName
	return nil
}

func (s *parityStore) DeleteGroup(groupPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[groupPath]; !ok {
		return errNotFound(groupPath)
	}
	delete(s.groups, groupPath)
	return nil
}

func (s *parityStore) transition(id string, to session.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return errNotFound(id)
	}
	sess.Status = to
	return nil
}

// nextDeterministicID returns sess-005, sess-006, ... so two parityStore
// instances exercised in lockstep generate the same ids and the parity
// comparison is meaningful.
func nextDeterministicID(counter *int) string {
	id := "sess-" + threeDigit(*counter)
	*counter++
	return id
}

func threeDigit(n int) string {
	if n < 0 {
		n = 0
	}
	d := []byte{'0', '0', '0'}
	for i := 2; i >= 0 && n > 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d)
}

func findSessionByID(snap *MenuSnapshot, id string) *MenuSession {
	if snap == nil {
		return nil
	}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeSession && item.Session != nil && item.Session.ID == id {
			return item.Session
		}
	}
	return nil
}

func findGroupByPath(snap *MenuSnapshot, path string) *MenuGroup {
	if snap == nil {
		return nil
	}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeGroup && item.Group != nil && item.Group.Path == path {
			return item.Group
		}
	}
	return nil
}

type parityErr string

func (e parityErr) Error() string { return string(e) }

func errNotFound(id string) error     { return parityErr("session/group not found: " + id) }
func errAlreadyExists(p string) error { return parityErr("already exists: " + p) }
