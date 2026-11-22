package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteTimelineRunner runs one negotiated read on a remote: the SSH
// runner's RunReadIO, which `agent-deck remote <name> recall timeline`
// already uses.
type remoteTimelineRunner interface {
	RunReadIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, unsupported []byte, follow bool, args ...string) error
}

func configuredRemoteTimelineRunner(name string) (remoteTimelineRunner, error) {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	remote, ok := cfg.Remotes[name]
	if !ok {
		return nil, errRemoteNotConfigured
	}
	return session.NewSSHRunner(name, remote), nil
}

var errRemoteNotConfigured = errors.New("remote is not configured")

// remoteTimelineTimeout bounds one remote read (a var so tests can shorten it).
var remoteTimelineTimeout = 30 * time.Second

const (
	// remoteTimelineMaxBytes bounds one remote answer. A page of rows fits
	// far below it; a larger answer is refused, not buffered.
	remoteTimelineMaxBytes = 8 << 20
	// remoteTimelineInFlight is how many timeline reads one remote serves at
	// once. Each is its own SSH connection plus the capability probe, so a
	// client polling several chats or stuck retrying cannot pile them up.
	remoteTimelineInFlight = 3
)

// remoteTimelineID is what a deck session id looks like. It keeps an id
// that starts with '-' from reading as an option on the remote.
var remoteTimelineID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// remoteTimelineCursor accepts only the base64url cursors `recall timeline`
// issues, so a cursor never carries a shell metacharacter or an option into
// the remote command line.
var remoteTimelineCursor = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// remoteTimelineUnsupported is what the runner prints when the remote's
// agent-deck has no `recall timeline` with these flags.
var remoteTimelineUnsupported = []byte(`{"code":"REMOTE_UNSUPPORTED"}` + "\n")

// cappedWriter fails a write past max instead of growing without bound.
type cappedWriter struct {
	buf    bytes.Buffer
	max    int
	capped bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		c.capped = true
		return 0, fmt.Errorf("remote answer exceeded %d bytes", c.max)
	}
	return c.buf.Write(p)
}

// handleRemoteSessionRoute serves /api/remotes/{name}/sessions/{id}/timeline.
func (s *Server) handleRemoteSessionRoute(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/remotes/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "sessions" || parts[2] == "" || parts[3] != "timeline" {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "route not found")
		return
	}
	s.handleRemoteSessionTimeline(w, r, parts[0], parts[2])
}

// handleRemoteSessionTimeline is GET /api/remotes/{name}/sessions/{id}/timeline:
// the remote's `agent-deck recall timeline <id> --json`, run over the
// remote's SSH runner with the same paging parameters as the local route,
// and answered as the same agent-deck.recall.rows/v2 object. The session
// must be one the remote fleet scan lists. The CLI's pane status is
// dropped, as on the local route.
func (s *Server) handleRemoteSessionTimeline(w http.ResponseWriter, r *http.Request, name, id string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	// Both sides must have recall on: turning it off here means no transcript
	// content leaves through this server, local or remote.
	if cfg, _ := session.LoadUserConfig(); cfg == nil || !cfg.Recall.GetEnabled() {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRecallDisabled, "recall is off on this machine: set [recall] enabled = true in config.toml (docs/recall.md)")
		return
	}
	if !remoteTimelineID.MatchString(id) {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "remote session not found")
		return
	}
	opts, err := timelineOptions(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	for _, cursor := range []string{opts.Since, opts.Before} {
		if cursor != "" && (len(cursor) > 64<<10 || !remoteTimelineCursor.MatchString(cursor)) {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid cursor")
			return
		}
	}
	if s.remoteFleet == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "remote fleet is unavailable")
		return
	}
	fleet := s.remoteFleet.Snapshot()
	if fleet.ObservedAt.IsZero() {
		// Right after start, before the first scan: not a missing session.
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRemoteOffline, "remotes not scanned yet")
		return
	}
	listed, known, online, tool := false, false, false, ""
	for _, remote := range fleet.Remotes {
		if remote.Name != name {
			continue
		}
		listed, online = true, remote.Online
		for _, inst := range remote.Sessions {
			if inst.ID == id {
				known, tool = true, inst.Tool
			}
		}
	}
	// Offline first: a remote whose first scan failed lists no sessions, and
	// a saved chat on it is unreachable for now, not gone.
	if listed && !online {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRemoteOffline, "remote is offline")
		return
	}
	if !known {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "remote session not found")
		return
	}
	if opts.AgentID != "" && session.RowsHarness(tool) != "claude" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "agent is supported for Claude Code sessions only")
		return
	}
	newRunner := s.remoteTimelineRunner
	if newRunner == nil {
		newRunner = configuredRemoteTimelineRunner
	}
	runner, err := newRunner(name)
	if errors.Is(err, errRemoteNotConfigured) {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "remote not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to open the remote")
		return
	}

	slots := s.remoteTimelineSlot(name)
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, ErrCodeRateLimited, "too many timeline reads in flight for this remote")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), remoteTimelineTimeout)
	defer cancel()
	stdout := &cappedWriter{max: remoteTimelineMaxBytes}
	stderr := &cappedWriter{max: 64 << 10}
	runErr := runner.RunReadIO(ctx, nil, stdout, stderr, remoteTimelineUnsupported, false, remoteTimelineArgs(id, opts)...)
	if stdout.capped {
		writeAPIError(w, http.StatusBadGateway, ErrCodeInternalError, "remote answer was too large")
		return
	}
	if runErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
		writeAPIError(w, http.StatusGatewayTimeout, ErrCodeInternalError, "remote timeline read timed out")
		return
	}
	if runErr != nil {
		writeRemoteTimelineFailure(w, stdout.buf.Bytes())
		return
	}
	var timeline query.RowsTimeline
	if err := json.Unmarshal(stdout.buf.Bytes(), &timeline); err != nil || timeline.Schema != query.RowsSchema || timeline.Session.ID != id {
		writeAPIError(w, http.StatusBadGateway, ErrCodeInternalError, "remote answered something other than this session's timeline")
		return
	}
	timeline.Status = nil
	if timeline.Turns == nil {
		timeline.Turns = []query.Row{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, timeline)
}

// remoteTimelineSlot is the in-flight semaphore for one remote. Only names
// the fleet scan lists get here, so the map stays as small as the config.
func (s *Server) remoteTimelineSlot(name string) chan struct{} {
	s.remoteTimelineMu.Lock()
	defer s.remoteTimelineMu.Unlock()
	if s.remoteTimelineSlots == nil {
		s.remoteTimelineSlots = make(map[string]chan struct{})
	}
	slots, ok := s.remoteTimelineSlots[name]
	if !ok {
		slots = make(chan struct{}, remoteTimelineInFlight)
		s.remoteTimelineSlots[name] = slots
	}
	return slots
}

// remoteTimelineArgs is the remote command line. Every value was validated
// before it gets here and rides in its flag as --flag=value, so a cursor
// that starts with '-' (base64url allows it) is never read as an option.
func remoteTimelineArgs(id string, opts query.RowsOptions) []string {
	args := []string{"recall", "timeline", "--json"}
	if opts.Tail > 0 {
		args = append(args, "--tail="+strconv.Itoa(opts.Tail))
	}
	if opts.Limit > 0 {
		args = append(args, "--limit="+strconv.Itoa(opts.Limit))
	}
	if opts.Since != "" {
		args = append(args, "--since="+opts.Since)
	}
	if opts.Before != "" {
		args = append(args, "--before="+opts.Before)
	}
	if opts.AgentID != "" {
		args = append(args, "--agent="+opts.AgentID)
	}
	return append(args, id)
}

// writeRemoteTimelineFailure maps the remote CLI's JSON error onto the
// local route's status codes.
func writeRemoteTimelineFailure(w http.ResponseWriter, out []byte) {
	var failure struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(bytes.TrimSpace(out), &failure)
	switch {
	case failure.Code == "REMOTE_UNSUPPORTED":
		writeAPIError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "the remote's agent-deck has no recall timeline; update it")
	case failure.Code == ErrCodeResyncRequired:
		writeAPIError(w, http.StatusConflict, ErrCodeResyncRequired, failure.Error)
	case strings.HasPrefix(failure.Error, "recall is off"):
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRecallDisabled, "recall is off on the remote: set [recall] enabled = true in its config.toml")
	case failure.Code == ErrCodeNotFound:
		// The remote's text can carry an absolute path on that host.
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "remote session or transcript not found")
	default:
		writeAPIError(w, http.StatusBadGateway, ErrCodeInternalError, "remote timeline read failed")
	}
}
