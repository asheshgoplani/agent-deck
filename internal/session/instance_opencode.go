package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// GetOpenCodePort returns the event-server port with lock protection.
func (i *Instance) GetOpenCodePort() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.OpenCodePort
}

func (i *Instance) setOpenCodePort(port int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.OpenCodePort = port
}

// UpdateOpenCodeSSEStatus feeds an SSE-derived status ("running"/"waiting")
// into the instance for the SSE fast path in UpdateStatus (issue #1614).
func (i *Instance) UpdateOpenCodeSSEStatus(status string, updatedAt time.Time) {
	if status == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	// A transition means new activity/output the user hasn't seen yet: reset
	// acknowledgment so waiting renders orange, mirroring UpdateHookStatus.
	if status != i.sseStatus && i.tmuxSession != nil {
		i.tmuxSession.ResetAcknowledged()
	}
	i.sseStatus = status
	i.sseLastUpdate = updatedAt
}

// DetectOpenCodeSession is the public wrapper for async OpenCode session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectOpenCodeSession() {
	i.detectOpenCodeSessionAsync()
}

// detectOpenCodeSessionAsync detects the OpenCode session ID after startup
// OpenCode generates session IDs internally (format: ses_XXXXX)
// We query the managed OpenCode HTTP server (or the bounded compatibility CLI
// fallback) and match by project directory, picking the most recently updated
// session since OpenCode auto-resumes the last session.
func (i *Instance) detectOpenCodeSessionAsync() {
	time.Sleep(1 * time.Second)

	// Phase 1: Quick detection for existing sessions (5 attempts over ~15s)
	quickDelays := []time.Duration{0, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}

	for attempt, delay := range quickDelays {
		if delay > 0 {
			time.Sleep(delay)
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug(
				"opencode_session_detected",
				slog.String("session_id", sessionID),
				slog.String("phase", "quick"),
				slog.Int("attempt", attempt+1),
			)
			return
		}

		sessionLog.Debug(
			"opencode_session_not_found",
			slog.Int("attempt", attempt+1),
			slog.Int("total", len(quickDelays)),
		)
	}

	// Phase 2: Long-running background watcher for new sessions
	// OpenCode only persists new sessions after significant user activity
	go i.watchForOpenCodeSession()
}

// watchForOpenCodeSession polls for session creation over an extended period.
// New sessions may take minutes to be persisted by OpenCode.
func (i *Instance) watchForOpenCodeSession() {
	const (
		pollInterval = 10 * time.Second
		maxDuration  = 5 * time.Minute
	)

	deadline := time.Now().Add(maxDuration)
	attempt := 0

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		attempt++

		i.mu.RLock()
		alreadyDetected := i.OpenCodeSessionID != ""
		i.mu.RUnlock()
		if alreadyDetected {
			sessionLog.Debug("opencode_watcher_already_set")
			return
		}

		if sessionID := i.queryOpenCodeSession(); sessionID != "" {
			i.setOpenCodeSession(sessionID)
			sessionLog.Debug(
				"opencode_watcher_detected",
				slog.String("session_id", sessionID),
				slog.Int("attempt", attempt),
			)
			return
		}

		sessionLog.Debug("opencode_watcher_not_found", slog.Int("attempt", attempt))
	}

	sessionLog.Debug("opencode_watcher_timeout", slog.Duration("max_duration", maxDuration))
}

// setOpenCodeSession sets the session ID and stores it in tmux environment.
func (i *Instance) setOpenCodeSession(sessionID string) {
	i.mu.Lock()
	i.setOpenCodeSessionLocked(sessionID)
	tmuxSession := i.tmuxSession
	i.mu.Unlock()
	i.syncOpenCodeSessionEnvironment(tmuxSession, sessionID)
}

// setOpenCodeSessionLocked updates in-memory binding state. The caller must
// hold i.mu for writing.
func (i *Instance) setOpenCodeSessionLocked(sessionID string) {
	i.OpenCodeSessionID = sessionID
	i.OpenCodeDetectedAt = time.Now()
	i.OpenCodeStartedAt = 0
}

func (i *Instance) syncOpenCodeSessionEnvironment(tmuxSession *tmux.Session, sessionID string) {
	if tmuxSession != nil {
		if err := tmuxSession.SetEnvironment("OPENCODE_SESSION_ID", sessionID); err != nil {
			sessionLog.Warn("opencode_set_env_failed", slog.String("error", err.Error()))
		}
	}
}

type openCodeSessionMetadata struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
}

type openCodeHTTPSessionMetadata struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Location  struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Time struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

func (s openCodeHTTPSessionMetadata) flatten() openCodeSessionMetadata {
	directory := s.Directory
	if directory == "" {
		directory = s.Location.Directory
	}
	return openCodeSessionMetadata{
		ID:        s.ID,
		Directory: directory,
		Path:      s.Path,
		Created:   s.Time.Created,
		Updated:   s.Time.Updated,
	}
}

// openCodeServiceSessionPage is one page of `opencode api session.list` on
// 2.x, where sessions live in the shared background service.
type openCodeServiceSessionPage struct {
	Data   []openCodeHTTPSessionMetadata `json:"data"`
	Cursor struct {
		Next string `json:"next"`
	} `json:"cursor"`
}

// The service pages at 50 by default and the directory filter still returns
// every sub-agent session, so one page can miss the root the TUI is in.
const openCodeServiceSessionPageSize = 200

// A page can be nothing but sub-agent sessions, so discovery follows the cursor
// until it holds a root and the bound session; the cap bounds a runaway store.
const openCodeServiceSessionMaxPages = 5

type openCodeCLIQueryCacheEntry struct {
	queriedAt time.Time
	sessions  []openCodeSessionMetadata
}

// findBestOpenCodeSession keeps an existing binding if that session still exists
// for the project. Fresh launches stay unbound until OpenCode persists a session
// created during the current startup, which prevents adopting older same-project
// sessions before the new conversation has an ID. Already-bound sessions only
// rotate to a newer sibling when there was very recent local pane activity,
// which approximates an intentional in-pane `/new` without stealing sessions
// from other tabs in the same project.
// sharedService is the OpenCode 2.x case: every TUI on the host shares one
// service, so the directory listing holds sibling conversations that other
// deck sessions are driving. An unbound instance then adopts only a session
// created after its own spawn; a sibling's fresh activity is not evidence.
func findBestOpenCodeSession(sessions []openCodeSessionMetadata, projectPath, currentID string, startedAt, activityAt int64, sharedService bool) string {
	if sharedService && currentID == "" && startedAt <= 0 {
		// Without a spawn time an unbound instance cannot tell its conversation from a sibling's.
		return ""
	}
	normalizedProjectPath := normalizePath(projectPath)

	var bestMatch string
	var bestMatchTime int64
	var currentMatchTime int64
	var currentExists bool
	var localRotationMatch string
	var localRotationTime int64
	startupThreshold := startedAt - opencodeStartupTimeSkew.Milliseconds()
	activityThreshold := activityAt - opencodeStartupTimeSkew.Milliseconds()

	for _, sess := range sessions {
		sessDir := sess.Directory
		if sessDir == "" {
			sessDir = sess.Path
		}

		if sessDir == "" || normalizePath(sessDir) != normalizedProjectPath {
			continue
		}

		// Multiple OpenCode tabs can share a project path. A newer sibling session
		// is not enough evidence to steal this instance's existing binding.
		updatedAt := sess.Updated
		if updatedAt == 0 {
			updatedAt = sess.Created
		}

		if currentID != "" && sess.ID == currentID {
			currentExists = true
			currentMatchTime = updatedAt
			if bestMatch == "" || updatedAt > bestMatchTime {
				bestMatch = sess.ID
				bestMatchTime = updatedAt
			}
			continue
		}

		if sharedService && currentID != "" {
			// The shared service lists every deck session's conversation in this
			// directory, so neither recent activity nor a missing binding makes a
			// sibling ours: keep the binding, or report nothing.
			continue
		}

		if currentID == "" && startedAt > 0 {
			if sharedService && sess.Created < startupThreshold {
				continue
			}
			if !sharedService && updatedAt < startupThreshold && sess.Created < startupThreshold {
				continue
			}
		}

		if currentID != "" && activityAt > 0 && (updatedAt >= activityThreshold || sess.Created >= activityThreshold) {
			if localRotationMatch == "" || updatedAt > localRotationTime {
				localRotationMatch = sess.ID
				localRotationTime = updatedAt
			}
		}

		if bestMatch == "" || updatedAt > bestMatchTime {
			bestMatch = sess.ID
			bestMatchTime = updatedAt
		}
	}

	if currentID != "" && currentExists {
		if localRotationMatch != "" && localRotationTime > currentMatchTime {
			return localRotationMatch
		}
		return currentID
	}

	return bestMatch
}

// queryOpenCodeSession queries the managed OpenCode HTTP server for sessions
// matching our project directory. Unbound instances adopt the most recently
// updated session, while already-bound instances keep their current ID as long
// as it still exists. Instances without a managed port retain a coalesced,
// rate-limited CLI compatibility path.
//
// CLI fallback wall-clock bounds:
//   - 5s context deadline for the subprocess itself.
//   - WaitDelay=500ms so cmd.Output() returns after the context fires even if
//     an opencode grandchild keeps stdout pipes open (Go 1.20+).
//
// 5s is the ceiling for cold opencode CLI on large session stores; on slower
// machines this still usually succeeds, and on genuine hangs the cache and
// lastOpenCodeScanAt schedule a later retry instead of spawning a poll storm.
func (i *Instance) queryOpenCodeSession() string {
	i.mu.RLock()
	port := i.OpenCodePort
	projectPath := i.ProjectPath
	currentID := i.OpenCodeSessionID
	startedAt := i.OpenCodeStartedAt
	lastStartedAt := i.LastStartedAt
	i.mu.RUnlock()

	sharedService := port == 0 && i.openCodeRejectsV1LaunchFlags()
	var minCreated int64
	if sharedService && currentID == "" {
		// OpenCodeStartedAt is not persisted, so a reloaded instance falls back to its last start.
		if startedAt <= 0 && !lastStartedAt.IsZero() {
			startedAt = lastStartedAt.UnixMilli()
		}
		if startedAt > 0 {
			minCreated = startedAt - opencodeStartupTimeSkew.Milliseconds()
		}
	}

	var sessions []openCodeSessionMetadata
	if port > 0 {
		var err error
		sessions, err = i.queryOpenCodeSessionsHTTP(port, projectPath)
		if err != nil {
			sessionLog.Debug("opencode_http_query_failed",
				slog.String("dir", logging.SanitizeValue(projectPath)),
				slog.Int("port", port),
				slog.String("error", err.Error()),
			)
			return ""
		}
	} else {
		sessions = i.queryOpenCodeSessionsCLI(projectPath, currentID, minCreated)
	}

	sessionLog.Debug("opencode_parsed_sessions", slog.Int("count", len(sessions)))

	var activityAt int64
	if currentID != "" {
		lastActivity := i.GetLastActivityTime()
		if !lastActivity.IsZero() && time.Since(lastActivity) <= opencodeRotationActivityWindow {
			activityAt = lastActivity.UnixMilli()
		}
	}

	bestMatch := findBestOpenCodeSession(sessions, projectPath, currentID, startedAt, activityAt, sharedService)
	sessionLog.Debug(
		"opencode_best_match",
		slog.String("session_id", bestMatch),
		slog.String("current_id", currentID),
	)
	return bestMatch
}

func (i *Instance) queryOpenCodeSessionsHTTP(port int, projectPath string) ([]openCodeSessionMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	endpoint := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:   "/session",
	}
	query := endpoint.Query()
	query.Set("directory", projectPath)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := openCodeSessionHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode session endpoint returned %s", resp.Status)
	}

	var payload []openCodeHTTPSessionMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, openCodeSessionResponseLimit)).Decode(&payload); err != nil {
		return nil, err
	}

	sessions := make([]openCodeSessionMetadata, 0, len(payload))
	for _, session := range payload {
		sessions = append(sessions, session.flatten())
	}
	return sessions, nil
}

// minCreated is the earliest creation time an unbound 2.x instance may adopt; 0 means any.
func (i *Instance) queryOpenCodeSessionsCLI(projectPath, currentID string, minCreated int64) []openCodeSessionMetadata {
	cacheKey := i.openCodeForkBinary() + "\x00" + normalizePath(projectPath)
	if i.openCodeRejectsV1LaunchFlags() {
		// The 2.x pager stops at what this caller can use, so a page set is only complete for that caller.
		cacheKey += "\x00" + currentID + "\x00" + strconv.FormatInt(minCreated, 10)
	}
	if sessions, ok := cachedOpenCodeCLISessions(cacheKey); ok {
		return sessions
	}

	result, _, _ := openCodeCLIQueryGroup.Do(cacheKey, func() (any, error) {
		if sessions, ok := cachedOpenCodeCLISessions(cacheKey); ok {
			return sessions, nil
		}
		sessions := i.runOpenCodeSessionsCLI(projectPath, currentID, minCreated)
		cacheOpenCodeCLISessions(cacheKey, sessions)
		return sessions, nil
	})
	sessions, ok := result.([]openCodeSessionMetadata)
	if !ok {
		return nil
	}
	return append([]openCodeSessionMetadata(nil), sessions...)
}

func cachedOpenCodeCLISessions(cacheKey string) ([]openCodeSessionMetadata, bool) {
	now := time.Now()
	openCodeCLIQueryCache.Lock()
	defer openCodeCLIQueryCache.Unlock()
	if cached, ok := openCodeCLIQueryCache.entries[cacheKey]; ok && now.Sub(cached.queriedAt) < opencodeRotationScanInterval {
		return append([]openCodeSessionMetadata(nil), cached.sessions...), true
	}
	return nil, false
}

func cacheOpenCodeCLISessions(cacheKey string, sessions []openCodeSessionMetadata) {
	now := time.Now()
	openCodeCLIQueryCache.Lock()
	defer openCodeCLIQueryCache.Unlock()
	for key, cached := range openCodeCLIQueryCache.entries {
		if now.Sub(cached.queriedAt) >= opencodeRotationScanInterval {
			delete(openCodeCLIQueryCache.entries, key)
		}
	}
	openCodeCLIQueryCache.entries[cacheKey] = openCodeCLIQueryCacheEntry{
		queriedAt: now,
		sessions:  append([]openCodeSessionMetadata(nil), sessions...),
	}
}

func (i *Instance) runOpenCodeSessionsCLI(projectPath, currentID string, minCreated int64) []openCodeSessionMetadata {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// On 2.x `session list` prints [] for a directory: the sessions live in the
	// shared background service and `opencode api` is the CLI's door to it.
	v2 := i.openCodeRejectsV1LaunchFlags()

	sessionLog.Debug("opencode_query_sessions",
		slog.String("dir", logging.SanitizeValue(projectPath)),
		slog.Bool("service_api", v2),
	)

	if v2 {
		return i.runOpenCodeServiceSessionPages(ctx, projectPath, currentID, minCreated)
	}

	output, ok := i.runOpenCodeCLI(ctx, projectPath, "session", "list", "--format", "json")
	if !ok {
		return nil
	}

	// Parse JSON response
	// Expected format: array of session objects with id, directory, created, updated fields
	var sessions []openCodeSessionMetadata

	if err := json.Unmarshal(output, &sessions); err != nil {
		sessionLog.Debug("opencode_parse_failed", slog.String("error", err.Error()))
		return nil
	}
	return sessions
}

func (i *Instance) runOpenCodeCLI(ctx context.Context, projectPath string, args ...string) ([]byte, bool) {
	cmd, err := i.openCodeCLICommand(ctx, projectPath, args...)
	if err != nil {
		sessionLog.Debug("opencode_query_failed", slog.String("error", err.Error()))
		return nil, false
	}

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			sessionLog.Warn("opencode_query_timeout",
				slog.String("dir", logging.SanitizeValue(projectPath)),
				slog.String("instance_id", i.ID),
			)
		} else {
			sessionLog.Debug("opencode_query_failed", slog.String("error", err.Error()))
		}
		return nil, false
	}

	sessionLog.Debug("opencode_session_data_size", slog.Int("bytes", len(output)))
	return output, true
}

// runOpenCodeServiceSessionPages walks `session.list` pages newest first and
// stops once it holds a root the caller can use: the bound session for a bound
// instance, else a root created at or after minCreated. Returning before the
// bound session is seen would rebind to a newer sibling.
// A failed page, or hitting the page cap before the bound session, returns
// nothing for the same reason.
func (i *Instance) runOpenCodeServiceSessionPages(ctx context.Context, projectPath, currentID string, minCreated int64) []openCodeSessionMetadata {
	var sessions []openCodeSessionMetadata
	cursor := ""
	usableSeen := false
	for page := 0; page < openCodeServiceSessionMaxPages; page++ {
		args := []string{"api", "session.list",
			"--param", "directory=" + projectPath,
			"--param", "limit=" + strconv.Itoa(openCodeServiceSessionPageSize)}
		if cursor != "" {
			args = append(args, "--param", "cursor="+cursor)
		}
		output, ok := i.runOpenCodeCLI(ctx, projectPath, args...)
		if !ok {
			return nil
		}
		roots, next, ok := parseOpenCodeServiceSessionPage(output)
		if !ok {
			return nil
		}
		for _, root := range roots {
			if (currentID != "" && root.ID == currentID) || (currentID == "" && root.Created >= minCreated) {
				usableSeen = true
			}
		}
		sessions = append(sessions, roots...)
		if next == "" || usableSeen {
			return sessions
		}
		cursor = next
	}
	if currentID != "" && !usableSeen {
		return nil
	}
	return sessions
}

// parseOpenCodeServiceSessionPage keeps root sessions only: a sub-agent
// session shares the directory and is updated more recently than the TUI's
// own, so it would win the best-match rotation and rebind the instance.
func parseOpenCodeServiceSessionPage(output []byte) ([]openCodeSessionMetadata, string, bool) {
	var page openCodeServiceSessionPage
	if err := json.Unmarshal(output, &page); err != nil {
		sessionLog.Debug("opencode_parse_failed", slog.String("error", err.Error()))
		return nil, "", false
	}
	sessions := make([]openCodeSessionMetadata, 0, len(page.Data))
	for _, session := range page.Data {
		if session.ParentID != "" {
			continue
		}
		sessions = append(sessions, session.flatten())
	}
	return sessions, page.Cursor.Next, true
}

// normalizePath normalizes a file path for comparison
func normalizePath(p string) string {
	// Expand home directory
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = strings.Replace(p, "~", home, 1)
		}
	}

	// Clean the path
	p = filepath.Clean(p)

	// Resolve symlinks if possible
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	return p
}

// CanForkOpenCode returns true if this OpenCode session can be forked
func (i *Instance) CanForkOpenCode() bool {
	sessionID, err := normalizeToolSessionID(FieldOpenCodeSessionID, i.OpenCodeSessionID)
	return i.Tool == "opencode" && err == nil && sessionID != "" && sessionID == strings.TrimSpace(i.OpenCodeSessionID) && time.Since(i.OpenCodeDetectedAt) < 5*time.Minute
}

// ForkOpenCode returns the command to create a forked OpenCode session.
// Uses OpenCode's native `--fork` flag to branch the parent session into a new
// session with its own id (discovered asynchronously after launch).
// Deprecated: Use ForkOpenCodeWithOptions instead.
func (i *Instance) ForkOpenCode(newTitle, newGroupPath string) (string, error) {
	return i.ForkOpenCodeWithOptions(newTitle, newGroupPath, nil)
}

// ForkOpenCodeWithOptions returns the command to create a forked OpenCode session
// with custom options. Uses OpenCode's native `opencode -s <parent-id> --fork`,
// which branches the parent transcript into a fresh session while leaving the
// parent intact, plus any model/agent flags.
func (i *Instance) ForkOpenCodeWithOptions(newTitle, newGroupPath string, opts *OpenCodeOptions) (string, error) {
	cmd, _, err := i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, i.ProjectPath)
	return cmd, err
}

// forkOpenCodeWithOptionsInWorkDir builds the one-time `cd <workDir> &&
// opencode -s <parent-id> --fork` launch command for a forked OpenCode
// instance. `--fork` is a newer OpenCode CLI flag that branches the session
// named by -s/--continue; if the installed binary predates it the launched
// command fails into a recoverable error state, mirroring how `codex fork` is
// handled (CanForkCodex below).
//
// The launch is explicitly anchored to workDir with a `cd`: the multi-repo fork
// path later repoints the tmux session WorkDir to the MultiRepoTempDir
// container (internal/ui/home.go), yet async OpenCode session detection matches
// by ProjectPath (DetectOpenCodeSession), so OpenCode must run in the requested
// repo/worktree dir — not tmux's WorkDir — for the child session to be
// discoverable. OpenCode mints the child session id, which that async detection
// picks up; the previous export/import clone relied on the same path (and the
// same `cd`), so no id is pre-assigned here. The env prefix is applied once by
// buildOpenCodeCommand at start time.
func (i *Instance) forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath string, opts *OpenCodeOptions, workDir string) (string, string, error) {
	if !i.CanForkOpenCode() {
		return "", "", fmt.Errorf("cannot fork: no active OpenCode session")
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}

	if i.openCodeRejectsV1LaunchFlags() {
		if i.IsSSH() || i.IsSandboxed() {
			return "", "", fmt.Errorf("opencode fork: service-based forks must run on the target host")
		}
		serviceOpts := opts
		if serviceOpts == nil {
			if config, err := LoadUserConfig(); err == nil && config != nil {
				serviceOpts = NewOpenCodeOptions(config)
			}
		}
		if serviceOpts != nil && serviceOpts.Model != "" {
			if _, err := openCodeModelRef(serviceOpts.Model); err != nil {
				return "", "", err
			}
		}
		// The 2.x verdict came from the configured binary, which a bare name may not reach.
		if _, ok := i.openCodeLaunchBinary(); !ok {
			return "", "", fmt.Errorf("opencode fork: cannot resolve the configured opencode binary")
		}
		binary := i.openCodeForkBinary()
		childID, err := i.forkOpenCodeSessionViaService(binary, i.OpenCodeSessionID, workDir)
		if err != nil {
			return "", "", err
		}
		err = i.moveOpenCodeForkViaService(binary, childID, workDir)
		if err == nil {
			err = applyOpenCodeForkOverridesViaService(binary, childID, workDir, serviceOpts)
		}
		if err == nil {
			err = setOpenCodeForkTitleViaService(binary, childID, workDir, newTitle)
		}
		if err != nil {
			if _, rmErr := runOpenCodeServiceCall(binary, workDir, "session.remove", childID, ""); rmErr != nil {
				return "", "", fmt.Errorf("%w (remove child session %s: %v)", err, childID, rmErr)
			}
			return "", "", err
		}
		// Launch through the selected launcher, as a restart does
		// (buildOpenCodeCommand): a bare `opencode` may be missing from PATH or
		// be a 1.x binary that does not know the 2.x child session.
		return fmt.Sprintf("cd %s && %s -s %s", shellescape.Quote(workDir), quoteOpenCodeArg(i.openCodeForkBinary()), shellescape.Quote(childID)), childID, nil
	}

	// Build extra flags from options (for fork, exclude session mode flags).
	var extraFlags string
	if opts != nil {
		for _, arg := range opts.ToArgsForFork() {
			extraFlags += " " + shellescape.Quote(arg)
		}
	} else if config, err := LoadUserConfig(); err == nil && config != nil {
		defaultOpts := NewOpenCodeOptions(config)
		for _, arg := range defaultOpts.ToArgsForFork() {
			extraFlags += " " + shellescape.Quote(arg)
		}
	}

	// workDir and the session id are shell-quoted to keep the launch command
	// injection-safe (the id is also charset-validated upstream by CanForkOpenCode).
	return fmt.Sprintf("cd %s && %s -s %s --fork%s",
		shellescape.Quote(workDir), quoteOpenCodeArg(i.openCodeForkBinary()), shellescape.Quote(i.OpenCodeSessionID), extraFlags), "", nil
}

// forkOpenCodeSessionViaService forks parentID in the OpenCode 2.x shared
// service, where `--fork` no longer exists, and returns the new session id.
// The child is a root session in the same directory, so the forked instance
// resumes it with a plain `-s` and a later restart does the same. The 1.x
// -m/--agent fork flags are not emitted: 2.x exits on them, so model and agent
// overrides are applied to the child session by applyOpenCodeForkOverridesViaService.
func (i *Instance) forkOpenCodeSessionViaService(binary, parentID, workDir string) (string, error) {
	output, err := runOpenCodeServiceCall(binary, workDir, "session.fork", parentID, "{}")
	if err != nil {
		return "", fmt.Errorf("opencode fork via service failed: %w", err)
	}

	var reply struct {
		Data openCodeHTTPSessionMetadata `json:"data"`
	}
	if err := json.Unmarshal(output, &reply); err != nil {
		return "", fmt.Errorf("opencode fork via service: unexpected reply: %w", err)
	}
	childID, err := normalizeToolSessionID(FieldOpenCodeSessionID, reply.Data.ID)
	if err != nil {
		return "", fmt.Errorf("opencode fork via service: %w", err)
	}
	if childID == "" {
		return "", fmt.Errorf("opencode fork via service: reply carries no session id")
	}
	sessionLog.Info("opencode_forked_via_service",
		slog.String("instance_id", i.ID),
		slog.String("parent_session_id", parentID),
		slog.String("child_session_id", childID))
	return childID, nil
}

// moveOpenCodeForkViaService moves a 2.x fork child onto workDir when the
// fork targets a different tree (a worktree fork). session.fork keeps the
// child in the parent's directory, so without the move only the shell cwd
// changes while the service session still belongs to the parent's tree. A
// same-directory fork skips the call.
func (i *Instance) moveOpenCodeForkViaService(binary, childID, workDir string) error {
	if filepath.Clean(workDir) == filepath.Clean(i.ProjectPath) {
		return nil
	}
	body, err := json.Marshal(map[string]string{"directory": workDir})
	if err != nil {
		return fmt.Errorf("opencode fork: encode move: %w", err)
	}
	if _, err := runOpenCodeServiceCall(binary, workDir, "session.move", childID, string(body)); err != nil {
		return fmt.Errorf("opencode fork: move child session %s to %s: %w", childID, workDir, err)
	}
	return nil
}

// applyOpenCodeForkOverridesViaService sets the fork's model and agent
// overrides on the 2.x child session. The service stores both on the session,
// so the first launch and every later `-s` resume pick them up.
func applyOpenCodeForkOverridesViaService(binary, childID, workDir string, opts *OpenCodeOptions) error {
	if opts == nil {
		return nil
	}
	if opts.Model != "" {
		ref, err := openCodeModelRef(opts.Model)
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]any{"model": ref})
		if err != nil {
			return fmt.Errorf("opencode fork: encode model override: %w", err)
		}
		if _, err := runOpenCodeServiceCall(binary, workDir, "session.switchModel", childID, string(body)); err != nil {
			return fmt.Errorf("opencode fork: set model on child session %s: %w", childID, err)
		}
	}
	if opts.Agent != "" {
		body, err := json.Marshal(map[string]string{"agent": opts.Agent})
		if err != nil {
			return fmt.Errorf("opencode fork: encode agent override: %w", err)
		}
		if _, err := runOpenCodeServiceCall(binary, workDir, "session.switchAgent", childID, string(body)); err != nil {
			return fmt.Errorf("opencode fork: set agent on child session %s: %w", childID, err)
		}
	}
	return nil
}

// openCodeModelRef splits a provider/model string into the 2.x service's
// model reference.
func openCodeModelRef(model string) (map[string]string, error) {
	providerID, modelID, ok := strings.Cut(model, "/")
	if !ok || providerID == "" || modelID == "" {
		return nil, fmt.Errorf("opencode fork: model %q is not in provider/model form", model)
	}
	modelID, variant, _ := strings.Cut(modelID, "#")
	if modelID == "" {
		return nil, fmt.Errorf("opencode fork: model %q has no model id", model)
	}
	ref := map[string]string{"providerID": providerID, "id": modelID}
	if variant != "" {
		ref["variant"] = variant
	}
	return ref, nil
}

// runOpenCodeServiceCall runs one `opencode api <operation>` request with the
// resolved opencode binary against the 2.x shared service for sessionID and
// returns its stdout. An empty body sends no request body.
func runOpenCodeServiceCall(binary, workDir, operation, sessionID, body string) ([]byte, error) {
	return runOpenCodeServiceCallContext(context.Background(), binary, workDir, operation, sessionID, body)
}

// runOpenCodeServiceCallContext is runOpenCodeServiceCall ended early when ctx is.
func runOpenCodeServiceCallContext(ctx context.Context, binary, workDir, operation, sessionID, body string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// #nosec G204 -- binary is the configured opencode resolved by
	// openCodeLaunchBinary, operation is a constant at every call site,
	// sessionID passed a shell-safe identifier check, and the JSON body is
	// passed as a single argv element.
	args := []string{"api", operation, "--param", "sessionID=" + sessionID}
	if body != "" {
		args = append(args, "-d", body)
	}
	inst := &Instance{Command: binary}
	cmd, err := inst.openCodeCLICommand(ctx, workDir, args...)
	if err != nil {
		return nil, err
	}
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return output, nil
}

// CreateForkedOpenCodeInstance creates a new Instance configured for forking an OpenCode session
// Deprecated: Use CreateForkedOpenCodeInstanceWithOptions instead.
func (i *Instance) CreateForkedOpenCodeInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedOpenCodeInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedOpenCodeInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, opts, i.ProjectPath, "", "")
}

// CreateForkedOpenCodeInstanceWithOptionsAndWorkDir creates a forked OpenCode instance
// rooted at workDir and copies worktree metadata when worktreeRepoRoot is set.
func (i *Instance) CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
	workDir, worktreeRepoRoot, worktreeBranch string,
) (*Instance, string, error) {
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}
	cmd, childID, err := i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, workDir)
	if err != nil {
		return nil, "", err
	}

	forked := NewInstance(newTitle, workDir)
	if childID != "" {
		forked.OpenCodeSessionID = childID
		forked.OpenCodeDetectedAt = time.Now()
	}
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.tmuxSession.SetGroupPath(forked.GroupPath)
	// Defer the one-shot fork script via ForkStartCommand (Pi/Codex pattern): the
	// script self-deletes after first run, so storing it as the persistent Command
	// would make a later restart re-run a missing file. Command holds a stable base
	// ("opencode") that restart resumes from via OpenCodeSessionID.
	forked.Command = i.openCodePersistentCommand()
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true
	forked.Tool = "opencode"
	if worktreeRepoRoot != "" {
		forked.WorktreePath = workDir
		forked.WorktreeRepoRoot = worktreeRepoRoot
		forked.WorktreeBranch = worktreeBranch
	}

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetOpenCodeOptions(opts); err != nil {
			sessionLog.Warn("set_opencode_options_failed", slog.String("error", err.Error()))
		}
	}

	return forked, cmd, nil
}
