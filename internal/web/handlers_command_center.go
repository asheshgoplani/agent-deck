package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// The Command Center is the embedded, live, two-way fleet god-view (see
// conductor/agent-deck/COMMAND-CENTER-DESIGN.md). v1 productizes the hand-made
// status HTMLs into a real agent-deck web panel:
//   - GET  /api/command-center/status  — synthesized cross-project snapshot
//   - GET  /events/command-center      — fingerprint-diffed SSE live push
//   - POST /api/command-center/ask     — two-way input routed via `session send`
//
// It composes the existing plumbing: it reads the same MenuSnapshot the menu
// handlers read (so it inherits the hook overlay + the menu change
// subscription that drives live updates), folds in each conductor's plain-
// language status artifacts from disk, and parses OPEN-ITEMS.md §D for the
// "decisions waiting on you" surface. No new daemon, no new auth — every
// endpoint is behind the existing authorize/CSRF/mutation/rate-limit gates.

var (
	commandCenterPollInterval      = 2 * time.Second
	commandCenterHeartbeatInterval = 15 * time.Second
)

// CommandCenterSnapshot is the see-everything payload rendered by the panel.
// Deliberately the same shape family as MenuSnapshot (profile + generatedAt +
// typed items) so the frontend's data conventions and SSE plumbing carry over.
type CommandCenterSnapshot struct {
	Profile     string `json:"profile"`
	GeneratedAt string `json:"generatedAt"`

	Conductors []CommandCenterConductor `json:"conductors"`
	Totals     CommandCenterTotals      `json:"totals"`

	// DecisionsWaiting is the ⭐ surface: rulings/reviews waiting on Ashesh,
	// parsed from OPEN-ITEMS.md §D. Empty when the file is absent.
	DecisionsWaiting []CommandCenterDecision `json:"decisionsWaiting"`

	// RecentlyCompleted carries the running→done/waiting transitions detected
	// since the previous snapshot, so the panel can fire "✅ X just finished"
	// notifications. It is diff-derived server-side and may be empty.
	RecentlyCompleted []CommandCenterCompletion `json:"recentlyCompleted"`

	// AskTargets is the server-authoritative allowlist of two-way input
	// targets (maestro + live conductor-* sessions). The panel populates its
	// target picker from this; the /ask handler re-validates against it.
	AskTargets []string `json:"askTargets"`
}

// CommandCenterConductor is one project/domain row: a conductor plus the live
// session list it manages, filtered to active work only.
type CommandCenterConductor struct {
	Name   string `json:"name"`   // short name, e.g. "agent-deck"
	Target string `json:"target"` // routing target, e.g. "conductor-agent-deck"
	// Status mirrors the conductor session's own status (running|waiting|idle
	// |error|...). "absent" when no conductor session exists for the group.
	Status string `json:"status"`
	// Substate is the honest-status v2 refinement of the conductor session.
	Substate string `json:"substate,omitempty"`
	// CurrentlyWorkingOn is plain-language, sourced from the conductor's
	// disk status artifact (status.json headline) or its latest prompt.
	CurrentlyWorkingOn string `json:"currentlyWorkingOn,omitempty"`
	// Sessions are the conductor's active children, error/stopped filtered out.
	Sessions []CommandCenterSession `json:"sessions"`
	Counts   CommandCenterCounts    `json:"counts"`
}

// CommandCenterSession is one active child session in a conductor's group.
type CommandCenterSession struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Substate string `json:"substate,omitempty"`
	// WorkingOn is the latest prompt/activity hint, when available.
	WorkingOn string `json:"workingOn,omitempty"`
}

// CommandCenterCounts is the per-conductor active-session tally (the noise the
// user explicitly does NOT want — error/stopped — is excluded by construction).
type CommandCenterCounts struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	Idle    int `json:"idle"`
}

// CommandCenterTotals is the fleet-wide active tally.
type CommandCenterTotals struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	Idle    int `json:"idle"`
}

// CommandCenterDecision is one item from OPEN-ITEMS.md §D.
type CommandCenterDecision struct {
	ID       string `json:"id"`       // e.g. "#1361"
	Question string `json:"question"` // the plain-language ask
	// Route is the conductor that owns the decision (best-effort; defaults to
	// the agent-deck conductor). Wires the two-way "answer the card" path.
	Route string `json:"route"`
}

// CommandCenterCompletion is a running→done/waiting transition for a session.
type CommandCenterCompletion struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // the new status (waiting|idle)
	At     string `json:"at"`
}

// askRequest is the POST /api/command-center/ask body.
type askRequest struct {
	Target  string `json:"target"` // "maestro" | "conductor-<name>" | "auto"
	Text    string `json:"text"`   // the instruction
	Context struct {
		DecisionID string `json:"decisionId"`
	} `json:"context,omitempty"`
}

// ccPrevStatuses tracks the last observed status per session id, per profile,
// so the SSE stream can diff running→done/waiting and emit completions. Guarded
// by the menu subscription serialization in the stream loop (one goroutine per
// connection); kept on the connection's stack, not shared, so no extra lock.
type ccStatusTracker map[string]string

// handleCommandCenterStatus serves a one-shot synthesized snapshot.
func (s *Server) handleCommandCenterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	snapshot, err := s.loadCommandCenterSnapshot(nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to build command center snapshot")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// handleCommandCenterEvents streams the synthesized snapshot live, copying the
// proven /events/menu fingerprint-diff + heartbeat pattern. It subscribes to
// the same menu change channel so a session transition pushes instantly.
func (s *Server) handleCommandCenterEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeStreamRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unavailable")
		return
	}

	tracker := ccStatusTracker{}
	snapshot, err := s.loadCommandCenterSnapshot(tracker)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to build command center snapshot")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastFingerprint := commandCenterFingerprint(snapshot)
	if err := writeSSEEvent(w, flusher, "command-center", snapshot); err != nil {
		return
	}

	menuChanges := s.subscribeMenuChanges()
	defer s.unsubscribeMenuChanges(menuChanges)

	pollTicker := time.NewTicker(commandCenterPollInterval)
	defer pollTicker.Stop()
	heartbeatTicker := time.NewTicker(commandCenterHeartbeatInterval)
	defer heartbeatTicker.Stop()

	ctx := r.Context()
	emitIfChanged := func() error {
		next, err := s.loadCommandCenterSnapshot(tracker)
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("command_center_stream_refresh_failed",
				slog.String("error", err.Error()))
			return nil
		}
		nextFingerprint := commandCenterFingerprint(next)
		// Always emit when a completion fired (the notification is the point),
		// even if the steady-state fingerprint (which excludes RecentlyCompleted)
		// is unchanged.
		if nextFingerprint == lastFingerprint && len(next.RecentlyCompleted) == 0 {
			return nil
		}
		if err := writeSSEEvent(w, flusher, "command-center", next); err != nil {
			return err
		}
		lastFingerprint = nextFingerprint
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		case _, ok := <-menuChanges:
			if !ok {
				// Subscription closed (server shutting down) — stop rather
				// than spin on a perpetually-ready closed channel.
				return
			}
			if err := emitIfChanged(); err != nil {
				return
			}
		case <-pollTicker.C:
			if err := emitIfChanged(); err != nil {
				return
			}
		}
	}
}

// loadCommandCenterSnapshot reads the menu snapshot (with hook overlay) and
// synthesizes the cross-project view. When tracker is non-nil it is updated
// with the latest per-session statuses and RecentlyCompleted is populated from
// the running→done/waiting diff.
func (s *Server) loadCommandCenterSnapshot(tracker ccStatusTracker) (*CommandCenterSnapshot, error) {
	menu, err := s.menuData.LoadMenuSnapshot()
	if err != nil {
		return nil, err
	}
	refreshSnapshotHookStatuses(menu, s.hookStatusLoader)
	return buildCommandCenterSnapshot(menu, s.cfg.Profile, conductorArtifactDir(), tracker), nil
}

// buildCommandCenterSnapshot is the pure synthesis function (no I/O beyond the
// optional artifactDir read), kept separate so it is unit-testable with fixed
// inputs. menu must already have the hook overlay applied.
func buildCommandCenterSnapshot(menu *MenuSnapshot, profile, artifactDir string, tracker ccStatusTracker) *CommandCenterSnapshot {
	snap := &CommandCenterSnapshot{
		Profile:           profile,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Conductors:        []CommandCenterConductor{},
		DecisionsWaiting:  []CommandCenterDecision{},
		RecentlyCompleted: []CommandCenterCompletion{},
		AskTargets:        []string{"maestro"},
	}
	if menu == nil {
		return snap
	}

	// Collect sessions, split conductors from children, group children by group.
	type sess struct {
		id, title, status, substate, group, prompt string
		isConductor                                bool
	}
	var all []sess
	conductorByGroup := map[string]sess{}
	for _, item := range menu.Items {
		if item.Type != "session" || item.Session == nil {
			continue
		}
		ms := item.Session
		e := sess{
			id:          ms.ID,
			title:       ms.Title,
			status:      string(ms.Status),
			substate:    ms.Substate,
			group:       ms.GroupPath,
			prompt:      firstLine(ms.LatestPrompt),
			isConductor: ms.IsConductor || strings.HasPrefix(ms.Title, "conductor-"),
		}
		all = append(all, e)
		if e.isConductor {
			// Map a conductor to the group it manages. Conductors named
			// "conductor-<name>" manage the "<name>" group by convention.
			managed := strings.TrimPrefix(e.title, "conductor-")
			conductorByGroup[managed] = e
		}
	}

	// Completion diff: running→(waiting|idle) since last snapshot. The tracker
	// is rebuilt each pass (not just upserted) so a session id that vanishes
	// from the registry is dropped — otherwise a later id reuse could compare
	// against a stale "running" and fire a false completion.
	if tracker != nil {
		next := make(map[string]string, len(all))
		for _, e := range all {
			if tracker[e.id] == "running" && (e.status == "waiting" || e.status == "idle") {
				snap.RecentlyCompleted = append(snap.RecentlyCompleted, CommandCenterCompletion{
					ID:     e.id,
					Title:  e.title,
					Status: e.status,
					At:     snap.GeneratedAt,
				})
			}
			next[e.id] = e.status
		}
		// Replace the tracker contents in place (the caller holds the map).
		for k := range tracker {
			delete(tracker, k)
		}
		for k, v := range next {
			tracker[k] = v
		}
	}

	// Build a conductor row per managed group. A group is a "conductor scope"
	// if there's a conductor-<group> session for it OR it simply has sessions.
	groupNames := map[string]bool{}
	for _, e := range all {
		if e.isConductor {
			groupNames[strings.TrimPrefix(e.title, "conductor-")] = true
		} else if e.group != "" {
			groupNames[e.group] = true
		}
	}
	names := make([]string, 0, len(groupNames))
	for n := range groupNames {
		names = append(names, n)
	}
	sort.Strings(names)

	statusFeeds := loadConductorStatusFeeds(artifactDir)

	for _, name := range names {
		cd := CommandCenterConductor{
			Name:     name,
			Target:   "conductor-" + name,
			Status:   "absent",
			Sessions: []CommandCenterSession{},
		}
		if c, ok := conductorByGroup[name]; ok {
			cd.Status = c.status
			cd.Substate = c.substate
			snap.AskTargets = append(snap.AskTargets, c.title)
		}
		// plain-language "currently working on": disk status feed wins, else
		// the conductor's own latest prompt.
		if feed, ok := statusFeeds[name]; ok && feed != "" {
			cd.CurrentlyWorkingOn = feed
		} else if c, ok := conductorByGroup[name]; ok {
			cd.CurrentlyWorkingOn = c.prompt
		}

		for _, e := range all {
			if e.isConductor || e.group != name {
				continue
			}
			// SEE-EVERYTHING but filter OUT the noise the user rejected.
			if e.status == "error" || e.status == "stopped" {
				continue
			}
			cd.Sessions = append(cd.Sessions, CommandCenterSession{
				ID:        e.id,
				Title:     e.title,
				Status:    e.status,
				Substate:  e.substate,
				WorkingOn: e.prompt,
			})
			switch e.status {
			case "running":
				cd.Counts.Running++
				snap.Totals.Running++
			case "waiting":
				cd.Counts.Waiting++
				snap.Totals.Waiting++
			case "idle":
				cd.Counts.Idle++
				snap.Totals.Idle++
			}
		}
		snap.Conductors = append(snap.Conductors, cd)
	}

	snap.DecisionsWaiting = parseDecisionsWaiting(artifactDir)
	return snap
}

// commandCenterFingerprint is the SSE diff key. It deliberately excludes
// RecentlyCompleted (a transient event list) and GeneratedAt (always changes)
// so the stream only re-emits on real fleet/decision state change.
func commandCenterFingerprint(snap *CommandCenterSnapshot) string {
	if snap == nil {
		return "nil"
	}
	payload := struct {
		Profile          string                   `json:"profile"`
		Conductors       []CommandCenterConductor `json:"conductors"`
		Totals           CommandCenterTotals      `json:"totals"`
		DecisionsWaiting []CommandCenterDecision  `json:"decisionsWaiting"`
	}{
		Profile:          snap.Profile,
		Conductors:       snap.Conductors,
		Totals:           snap.Totals,
		DecisionsWaiting: snap.DecisionsWaiting,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "marshal-error"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// conductorArtifactDir resolves the on-disk conductor/ directory (where each
// conductor keeps state.json / status.json / OPEN-ITEMS.md). Returns "" if it
// can't be resolved, in which case the synthesis falls back to live-only data.
func conductorArtifactDir() string {
	dir, err := agentpaths.EffectiveDataPath("conductor", "conductor")
	if err != nil {
		return ""
	}
	return dir
}

// loadConductorStatusFeeds reads each conductor's optional structured
// status.json (the v1.x hardening contract) for a plain-language headline,
// keyed by conductor short name. Free-text fallback (state.json) is omitted in
// v1 to avoid brittle parsing; conductors that haven't adopted status.json
// simply fall back to the live latest-prompt.
func loadConductorStatusFeeds(artifactDir string) map[string]string {
	feeds := map[string]string{}
	if artifactDir == "" {
		return feeds
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return feeds
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		raw, err := os.ReadFile(filepath.Join(artifactDir, name, "status.json"))
		if err != nil {
			continue
		}
		var parsed struct {
			Headline string `json:"headline"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.Headline != "" {
			feeds[name] = firstLine(parsed.Headline)
		}
	}
	return feeds
}

// parseDecisionsWaiting reads conductor/agent-deck/OPEN-ITEMS.md and extracts
// the §D "DECISIONS — waiting on Ashesh" items. It handles both shapes seen in
// practice: a single "· "-separated line, and a normal multi-line Markdown list
// ("- item" / "* item"). Every non-empty line until the next "## " section is
// consumed; list markers are stripped; each line is further split on "·".
// Best-effort: returns empty on any failure.
func parseDecisionsWaiting(artifactDir string) []CommandCenterDecision {
	out := []CommandCenterDecision{}
	if artifactDir == "" {
		return out
	}
	f, err := os.Open(filepath.Join(artifactDir, "agent-deck", "OPEN-ITEMS.md"))
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "## ") {
			// Section D begins with "## D. DECISIONS"; any later "## " ends it.
			inSection = strings.HasPrefix(line, "## D.")
			continue
		}
		if !inSection || line == "" {
			continue
		}
		// Strip a leading Markdown list marker ("- " / "* ") if present.
		line = strings.TrimSpace(strings.TrimLeft(line, "-*"))
		for _, raw := range strings.Split(line, "·") {
			item := strings.TrimSpace(raw)
			if item == "" {
				continue
			}
			id := ""
			if strings.HasPrefix(item, "#") {
				if sp := strings.IndexByte(item, ' '); sp > 0 {
					id = item[:sp]
				}
			}
			out = append(out, CommandCenterDecision{
				ID:       id,
				Question: item,
				Route:    "conductor-agent-deck",
			})
		}
	}
	return out
}

// handleCommandCenterAsk routes a typed instruction to Maestro or a chosen
// conductor through the supported `session send` primitive — the same safe,
// non-invasive inbound path bridge.py and the voice receiver use. It does NOT
// type into tmux directly and does NOT touch the bridge.
func (s *Server) handleCommandCenterAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	// /ask can move the fleet — gate it as a mutation (403 in --read-only or
	// when web mutations are disabled) and rate-limit it like every WebMutator.
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}

	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "text is required")
		return
	}

	// Resolve and validate the target against the server-authoritative
	// allowlist (maestro + live conductor-* sessions). No arbitrary injection.
	snapshot, err := s.loadCommandCenterSnapshot(nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve targets")
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" || target == "auto" {
		// Default to Maestro (classify+delegate) when present, else the
		// agent-deck conductor.
		target = "maestro"
		if !targetAllowed(snapshot.AskTargets, "maestro") {
			target = "conductor-agent-deck"
		}
	}
	resolved := resolveAskTarget(target, snapshot.AskTargets)
	if resolved == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_TARGET", "unknown target")
		return
	}

	// Tag the message like every other inbound channel so the receiving
	// conductor knows the source. text is passed as a single argv element to
	// `session send` — never interpolated into a shell string — closing the
	// multiline / metacharacter footgun.
	msg := "[command-center] " + text

	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "agent-deck"
	}
	// --no-wait so the HTTP request returns promptly (the reply reflects back
	// via the SSE feed as the fleet moves); -p <profile> so it works headless.
	// Bound the child with a context timeout so a stalled `session send` (e.g.
	// a wedged tmux) can't pile up orphaned processes if many asks fire.
	args := []string{"-p", s.cfg.Profile, "session", "send", resolved, msg, "--no-wait"}
	cmdCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, exe, args...)
	cmd.Env = os.Environ()
	out, runErr := cmd.CombinedOutput()

	// Audit: journal the routing decision (correlationId + target + text-hash,
	// never the raw text) — mirrors Maestro's outbox audit pattern.
	correlationID := fmt.Sprintf("cc-%d", time.Now().UnixNano())
	textHash := sha256.Sum256([]byte(text))
	logging.ForComponent(logging.CompWeb).Info("command_center_ask",
		slog.String("correlationId", correlationID),
		slog.String("target", resolved),
		slog.String("textHash", hex.EncodeToString(textHash[:8])),
	)

	if runErr != nil {
		logging.ForComponent(logging.CompWeb).Warn("command_center_ask_send_failed",
			slog.String("target", resolved),
			slog.String("error", runErr.Error()),
			slog.String("output", strings.TrimSpace(string(out))),
		)
		writeAPIError(w, http.StatusBadGateway, "SEND_FAILED", "failed to deliver to target")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"correlationId": correlationID,
		"routedTo":      resolved,
	})
}

// resolveAskTarget maps a requested target to the concrete allowlisted session
// title, returning "" if it is not allowed. "maestro" resolves to the live
// "conductor-maestro" session when present (else bare "maestro").
func resolveAskTarget(target string, allow []string) string {
	if target == "maestro" {
		if targetAllowed(allow, "conductor-maestro") {
			return "conductor-maestro"
		}
		if targetAllowed(allow, "maestro") {
			return "maestro"
		}
		return ""
	}
	if targetAllowed(allow, target) {
		return target
	}
	return ""
}

func targetAllowed(allow []string, target string) bool {
	for _, a := range allow {
		if a == target {
			return true
		}
	}
	return false
}

// firstLine returns the first non-empty line of s, trimmed and length-capped,
// for compact plain-language display.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	const max = 160
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
