package web

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

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
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRecallDisabled, "recall is off: set [recall] enabled = true in config.toml (docs/recall.md)")
		return
	}
	opts, err := timelineOptions(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	// `agent-deck web` wraps its SessionDataService in a MemoryMenuData
	// (cmd/agent-deck/main.go), which does not expose the instances, so the
	// route opens the server's profile itself. Tests set a
	// SessionDataService with fake storage instead.
	service, ok := s.menuData.(*SessionDataService)
	if !ok {
		service = NewSessionDataService(s.cfg.Profile)
	}
	inst, peers, err := loadTimelineSession(service, sessionID)
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
		writeAPIError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "timeline is available for Claude Code, Codex and Pi sessions only")
		return
	case errors.Is(err, session.ErrNoNativeTranscript):
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		return
	case err != nil:
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to resolve the session transcript")
		return
	}
	if opts.AgentID != "" && src.Harness != "claude" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "agent is supported for Claude Code sessions only")
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
func loadTimelineSession(service *SessionDataService, id string) (*session.Instance, []*session.Instance, error) {
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

// timelineAgentID is what a Claude Code sub-agent id looks like; anything
// else in the agent parameter is refused before it names a file.
var timelineAgentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// timelineOptions reads the CLI's paging flags from the query string and
// rejects combinations query.ValidateRowsOptions refuses.
func timelineOptions(r *http.Request) (query.RowsOptions, error) {
	q := r.URL.Query()
	opts := query.RowsOptions{Since: q.Get("since"), Before: q.Get("before"), AgentID: q.Get("agent")}
	if opts.AgentID != "" && !timelineAgentID.MatchString(opts.AgentID) {
		return opts, errors.New("agent must be a sub-agent id (up to 128 letters, digits, '-' and '_')")
	}
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
