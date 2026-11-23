package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/todos"
)

// The owner's to-do list over HTTP (docs/todos.md), so the phone gateway
// and the Mac app show the same list as `agent-deck todo`:
//
//	GET  /api/todos[?all=1]       todos.ListResult
//	POST /api/todos               {"text","source"?,"session_id"?} -> 201 todos.Result
//	POST /api/todos/{id}/done[?source=mac]    todos.Result (done | already_done), 404 unknown id
//	POST /api/todos/{id}/undo[?source=mac]    todos.Result (reopened | already_open), 404 unknown id
//	POST /api/todos/{id}/remove[?source=mac]  todos.Result (removed), 404 unknown id
//
// Reads need the owner token like every /api route. Writes are refused in
// read-only mode and when web mutations are off, and share the mutation
// rate limit. Every change publishes todo.changed (todos.Publish).

// todoBodyLimit caps a POST body: the text is at most 500 characters.
const todoBodyLimit = 16 << 10

// todoStoreOpen opens the host's list; tests swap it.
var todoStoreOpen = todos.Open

// todoPublish publishes a change; tests swap it.
var todoPublish = func(c todos.Change, profile string) { todos.Publish(c, profile) }

type createTodoRequest struct {
	Text      string `json:"text"`
	Source    string `json:"source"`
	SessionID string `json:"session_id"`
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("/api/todos", s.handleTodos)
		mux.HandleFunc("/api/todos/{id}/{action}", s.handleTodoAction)
	})
}

// checkTodoWrite applies the read-only mode, the mutations switch and the
// rate limit to a write.
func (s *Server) checkTodoWrite(w http.ResponseWriter) bool {
	if s.cfg.ReadOnly {
		writeAPIError(w, http.StatusForbidden, ErrCodeReadOnly, "the server is read-only")
		return false
	}
	return s.checkMutationsAllowed(w) && s.checkMutationRateLimit(w)
}

func (s *Server) handleTodos(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		store, err := todoStoreOpen()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to open the to-do list")
			return
		}
		all := r.URL.Query().Get("all")
		res, err := store.ListResult(all == "1" || strings.EqualFold(all, "true"))
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to read the to-do list")
			return
		}
		writeJSON(w, http.StatusOK, res)
	case http.MethodPost:
		if !s.checkTodoWrite(w) {
			return
		}
		var req createTodoRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, todoBodyLimit))
		if err := dec.Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
			return
		}
		if req.Source == "" {
			req.Source = todos.SourcePhone
		}
		store, err := todoStoreOpen()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to open the to-do list")
			return
		}
		it, err := store.Add(req.Text, todos.AddOptions{Source: req.Source, SessionID: req.SessionID})
		switch {
		case errors.Is(err, todos.ErrEmptyText), errors.Is(err, todos.ErrTextTooLong),
			errors.Is(err, todos.ErrBadSource), errors.Is(err, todos.ErrBadSession):
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
			return
		case errors.Is(err, todos.ErrNewerVersion):
			writeAPIError(w, http.StatusConflict, ErrCodeBadRequest, err.Error())
			return
		case err != nil:
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to save the to-do list")
			return
		}
		res := todos.Result{Status: todos.StatusAdded, Todo: &it, Open: store.OpenCount()}
		todoPublish(todos.NewChange(todos.ActionAdd, it, derefOpen(res.Open), it.Source), s.cfg.Profile)
		writeJSON(w, http.StatusCreated, res)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}

// handleTodoAction is POST /api/todos/{id}/done|undo|remove: tick off,
// reopen (a wrong tap on the phone) or delete one item by id.
func (s *Server) handleTodoAction(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.PathValue("action") {
	case todos.ActionDone, todos.ActionUndo, todos.ActionRemove:
	default:
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "no such to-do action")
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.checkTodoWrite(w) {
		return
	}
	by := r.URL.Query().Get("source")
	if by == "" {
		by = todos.SourcePhone
	}
	if !todos.ValidSource(by) {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, todos.ErrBadSource.Error())
		return
	}
	id := r.PathValue("id")
	if !todos.IsID(id) {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, todos.ErrBadID.Error())
		return
	}
	store, err := todoStoreOpen()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to open the to-do list")
		return
	}
	action := r.PathValue("action")
	var (
		it      todos.Item
		changed bool
	)
	switch action {
	case todos.ActionDone:
		it, changed, err = store.Done(id)
	case todos.ActionUndo:
		it, changed, err = store.Undo(id)
	case todos.ActionRemove:
		it, err = store.Remove(id)
		changed = err == nil
	}
	switch {
	case errors.Is(err, todos.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "no to-do with that id")
		return
	case errors.Is(err, todos.ErrNewerVersion):
		writeAPIError(w, http.StatusConflict, ErrCodeBadRequest, err.Error())
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to save the to-do list")
		return
	}
	res := todos.Result{Todo: &it, Open: store.OpenCount()}
	switch {
	case action == todos.ActionDone && changed:
		res.Status = todos.StatusDone
	case action == todos.ActionDone:
		res.Status = todos.StatusAlreadyDone
	case action == todos.ActionUndo && changed:
		res.Status = todos.StatusReopened
	case action == todos.ActionUndo:
		res.Status = todos.StatusAlreadyOpen
	default:
		res.Status = todos.StatusRemoved
	}
	if changed {
		todoPublish(todos.NewChange(action, it, derefOpen(res.Open), by), s.cfg.Profile)
	}
	writeJSON(w, http.StatusOK, res)
}

func derefOpen(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
