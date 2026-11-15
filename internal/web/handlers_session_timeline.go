package web

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ErrCodeRecallDisabled answers the timeline route while [recall] enabled
// is false, the same gate `agent-deck recall timeline` applies.
const ErrCodeRecallDisabled = "RECALL_DISABLED"

// ErrCodeResyncRequired answers a since/before cursor that no longer
// describes the session's transcript. The client reloads without a cursor.
const ErrCodeResyncRequired = "RESYNC_REQUIRED"

// handleSessionTimeline is GET /api/sessions/{id}/timeline, the HTTP form
// of `agent-deck recall timeline <id> --json`. It answers the same
// agent-deck.recall.rows/v2 object, read straight from the session's native
// Claude Code or Codex transcript, with the same paging parameters:
// since, before (with limit), limit, tail and agent. The live pane status
// the CLI adds is left out; the menu stream already carries session status.
func (s *Server) handleSessionTimeline(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if cfg, _ := session.LoadUserConfig(); cfg == nil || !cfg.Recall.GetEnabled() {
		writeAPIError(w, http.StatusConflict, ErrCodeRecallDisabled, "recall is off: set [recall] enabled = true in config.toml (docs/recall.md)")
		return
	}
	opts, err := timelineOptions(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	inst, peers, err := s.loadTimelineSession(sessionID)
	if err != nil {
		if errors.Is(err, errTimelineSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load session data")
		return
	}
	src, sess, err := session.LiveRowsSource(inst, peers)
	switch {
	case errors.Is(err, query.ErrRowsUnsupported):
		writeAPIError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "timeline is not available for "+inst.Tool+" sessions")
		return
	case errors.Is(err, session.ErrNoNativeTranscript):
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	timeline, err := query.ReadRows(r.Context(), src, opts)
	if err != nil {
		var resync *query.ResyncError
		switch {
		case errors.As(err, &resync):
			writeAPIError(w, http.StatusConflict, ErrCodeResyncRequired, err.Error())
		case errors.Is(err, os.ErrNotExist):
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "transcript not found")
		default:
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to read the session transcript")
		}
		return
	}
	timeline.Schema, timeline.Session, timeline.Source = query.RowsSchema, sess, "native"
	if timeline.Turns == nil {
		timeline.Turns = []query.Row{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, timeline)
}

var errTimelineSessionNotFound = errors.New("session not found")

// loadTimelineSession loads the session and its profile peers, which
// resolving a Codex rollout needs to skip threads bound to other sessions.
func (s *Server) loadTimelineSession(id string) (*session.Instance, []*session.Instance, error) {
	service, ok := s.menuData.(*SessionDataService)
	if !ok {
		service = NewSessionDataService(s.cfg.Profile)
	}
	storage, _, err := service.resolveAndOpenStorage()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = storage.Close() }()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil, nil, err
	}
	for _, inst := range instances {
		if inst.ID == id {
			return inst, instances, nil
		}
	}
	return nil, nil, errTimelineSessionNotFound
}

// timelineOptions reads the CLI's paging flags from the query string and
// rejects combinations query.ValidateRowsOptions refuses.
func timelineOptions(r *http.Request) (query.RowsOptions, error) {
	q := r.URL.Query()
	opts := query.RowsOptions{Since: q.Get("since"), Before: q.Get("before"), AgentID: q.Get("agent")}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"limit", &opts.Limit}, {"tail", &opts.Tail}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, errors.New(p.name + " must be an integer")
			}
			*p.dst = n
		}
	}
	return opts, query.ValidateRowsOptions(opts)
}
