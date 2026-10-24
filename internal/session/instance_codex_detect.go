package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/procfd"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// codexRolloutExistsInHome reports whether Codex has flushed a rollout JSONL
// for the given session ID under codexHome/sessions. Used by buildCodexCommand
// to gate `codex resume <sid>` on a real on-disk rollout file (Issue #756).
//
// Codex layout: codexHome/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
func codexRolloutExistsInHome(sessionID, codexHome string) bool {
	return codexRolloutPathInHome(sessionID, codexHome) != ""
}

// DetectCodexSession is the public wrapper for async Codex session detection
// Call this for restored sessions that don't have a session ID yet
func (i *Instance) DetectCodexSession() {
	i.detectCodexSessionAsync()
}

// detectCodexSessionAsync detects the Codex session ID after startup
// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Session ID is a UUID that can be extracted from the filename
// Since Codex has no "session list" command, we scan the filesystem
func (i *Instance) resolveCodexDetectionCandidate(sessionID string, probeErr error) string {
	sessionID = i.filterCodexProcessProbeCandidate(sessionID)
	if sessionID == "" && probeErr == nil {
		if threadID, live := i.liveCodexBootstrapEvidence(nil); live {
			i.recordCodexOwnership(threadID)
			return threadID
		}
		var pass StatusUpdatePass
		// Warm evidence before serializing selection; no subprocess holds the
		// bootstrap mutex, including asynchronous startup detection.
		i.codexExclusions(&pass)
		codexBootstrapMu.Lock()
		defer codexBootstrapMu.Unlock()
		exclude := i.codexExclusions(&pass)
		if exclude == nil {
			return ""
		}
		sessionID = i.queryCodexSession(exclude, true)
		i.recordCodexOwnership(sessionID)
		return sessionID
	}
	i.recordCodexOwnership(sessionID)
	return sessionID
}

func (i *Instance) detectCodexSessionAsync() {
	// Brief wait for Codex to initialize
	time.Sleep(1 * time.Second)

	// Try up to 3 times with short delays
	delays := []time.Duration{0, 1 * time.Second, 2 * time.Second}

	for attempt, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}

		sessionID, _, probeErr := i.queryCodexSessionFromProcessFiles(nil)
		sessionID = i.resolveCodexDetectionCandidate(sessionID, probeErr)
		if sessionID != "" {
			i.CodexSessionID = sessionID
			i.CodexDetectedAt = time.Now()

			// Store in tmux environment for restart
			if i.tmuxSession != nil {
				if err := i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", sessionID); err != nil {
					sessionLog.Warn("codex_set_env_failed", slog.String("error", err.Error()))
				}
			}

			sessionLog.Debug(
				"codex_session_detected",
				slog.String("session_id", sessionID),
				slog.Int("attempt", attempt+1),
			)
			return
		}

		if probeErr != nil {
			sessionLog.Debug("codex_session_probe_undetermined", slog.Int("attempt", attempt+1), slog.Any("error", probeErr))
		} else {
			sessionLog.Debug("codex_session_not_found", slog.Int("attempt", attempt+1), slog.Int("total", len(delays)))
		}
	}

	sessionLog.Warn("codex_detection_failed", slog.Int("attempts", len(delays)))
}

func getCodexHomeDir() string {
	return sandboxedToolDir("Codex home", getCodexHomeDirUnguarded())
}

func getCodexHomeDirUnguarded() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return ExpandPath(codexHome)
	}

	if cfg, err := LoadUserConfig(); err == nil && cfg != nil {
		profile := resolvedProcessProfile()
		if profileDir := cfg.GetProfileCodexConfigDir(profile); profileDir != "" {
			return profileDir
		}
		if cfg.Codex.ConfigDir != "" {
			return ExpandPath(cfg.Codex.ConfigDir)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex")
	}
	return filepath.Join(home, ".codex")
}

func isCodexHomeExplicit() bool {
	if strings.TrimSpace(os.Getenv("CODEX_HOME")) != "" {
		return true
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return false
	}
	profile := resolvedProcessProfile()
	if cfg.GetProfileCodexConfigDir(profile) != "" {
		return true
	}
	return strings.TrimSpace(cfg.Codex.ConfigDir) != ""
}

// runWithTimeout runs op in a goroutine and waits up to timeout for it to
// complete. Returns true if op finished, false if it timed out. The
// abandoned goroutine continues running until op returns naturally; its
// effects on shared state after timeout are not consulted by callers, which
// must check the return value before reading any variables op may have
// written.
//
// Used to backstop FS operations under ~/.codex/sessions which can hang
// indefinitely on a stuck FS layer (kernel D-state during readdir on the
// WSL 9p path was observed on 2026-04-28; one thread held a dentry that
// the FS layer never released, blocking every agent-deck CLI command that
// transitively walked the codex sessions tree).
func runWithTimeout(timeout time.Duration, op func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// codexWalkDirTimeout caps the recursive walk over $CODEX_HOME/sessions/ in
// queryCodexSession. Healthy walks of a year's sessions complete in roughly
// one second; the bound is generous so a slow disk does not false-negative
// while still preventing indefinite hangs.
const codexWalkDirTimeout = 5 * time.Second

// queryCodexSession scans Codex sessions and returns the best candidate.
// Selection strategy:
//  1. Prefer sessions whose JSONL metadata matches this instance's project path.
//  2. Optionally allow unscoped fallback (no cwd metadata) for initial bootstrap.
func (i *Instance) queryCodexSession(excludeIDs map[string]bool, allowUnscoped bool) string {
	return queryCodexSessionWith(i.getCodexHomeDir(), i.ProjectPath, i.CodexStartedAt, i.ID, excludeIDs, allowUnscoped)
}

// codexWalkDir walks $CODEX_HOME/sessions during bootstrap discovery. Test seam.
var codexWalkDir = filepath.WalkDir

// queryCodexSessionWith is queryCodexSession over explicit inputs, so the
// status pass can run the (up to codexWalkDirTimeout) walk without holding
// i.mu, and a timed-out walk goroutine never reads Instance fields (#2674).
func queryCodexSessionWith(codexHome, projectPath string, codexStartedAt int64, instanceID string, excludeIDs map[string]bool, allowUnscoped bool) string {
	sessionsDir := filepath.Join(codexHome, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return ""
	}

	uuidPattern := uuidPatternRE

	var bestScopedID string
	var bestScopedTime time.Time
	var bestUnscopedID string
	var bestUnscopedTime time.Time

	normalizedProjectPath := normalizePath(projectPath)

	var walkErr error
	if !runWithTimeout(codexWalkDirTimeout, func() {
		walkErr = codexWalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors
			}

			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}

			sessionID := uuidPattern.FindString(d.Name())
			if sessionID == "" {
				return nil
			}
			if excludeIDs != nil && excludeIDs[sessionID] {
				return nil
			}

			// Subagent-thread gate (incident 2026-07-15): never let the
			// bootstrap disk scan adopt a subagent rollout as the session's
			// main thread — codex refuses user turns on it. Skipping it here
			// (rather than after selection) lets the walk fall through to the
			// best user-sourced match instead of returning nothing when the
			// most-recent match happens to be a subagent. See
			// codex_subagent_gate.go.
			if CodexSubagentThread(sessionID, codexHome) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			// Only consider sessions created after we started this instance.
			if codexStartedAt > 0 {
				startTime := time.UnixMilli(codexStartedAt)
				if info.ModTime().Before(startTime) {
					return nil
				}
			}

			matchesProject, hasProjectMetadata := codexSessionMatchesProject(path, normalizedProjectPath)
			if matchesProject {
				if bestScopedID == "" || info.ModTime().After(bestScopedTime) {
					bestScopedID = sessionID
					bestScopedTime = info.ModTime()
				}
				return nil
			}

			// Use unscoped records only when bootstrapping and metadata is unavailable.
			if allowUnscoped && !hasProjectMetadata {
				if bestUnscopedID == "" || info.ModTime().After(bestUnscopedTime) {
					bestUnscopedID = sessionID
					bestUnscopedTime = info.ModTime()
				}
			}

			return nil
		})
	}) {
		// Walk did not complete in time. The most likely cause is a stuck FS
		// layer (e.g., WSL kernel D-state). Return without consulting the
		// best* variables since they may be partially populated; the caller
		// retries with backoff so a transient stall self-heals.
		sessionLog.Warn("codex_walkdir_timeout",
			slog.String("instance_id", instanceID),
			slog.String("sessions_dir", sessionsDir),
			slog.Duration("timeout", codexWalkDirTimeout))
		return ""
	}
	if walkErr != nil {
		sessionLog.Debug("codex_scan_error", slog.String("error", walkErr.Error()))
	}

	if bestScopedID != "" {
		return bestScopedID
	}
	if allowUnscoped {
		return bestUnscopedID
	}
	return ""
}

// codexSessionMatchesProject checks whether a Codex session file belongs to the
// current project by inspecting JSONL metadata fields (cwd/workdir/path).
// Returns:
//   - match=true if project path matches
//   - known=true if any project metadata field was found
func codexSessionMatchesProject(sessionFilePath, normalizedProjectPath string) (match bool, known bool) {
	if normalizedProjectPath == "" {
		return false, false
	}

	file, err := os.Open(sessionFilePath)
	if err != nil {
		return false, false
	}
	defer file.Close()

	const maxLines = 256

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineCount := 0
	foundMetadata := false

	for scanner.Scan() {
		lineCount++
		cwd := extractCodexCWDFromJSONLine(scanner.Bytes())
		if cwd != "" {
			foundMetadata = true
			if normalizePath(cwd) == normalizedProjectPath {
				return true, true
			}
		}
		if lineCount >= maxLines {
			break
		}
	}

	return false, foundMetadata
}

// extractCodexCWDFromJSONLine extracts cwd-like project fields from one JSONL record.
func extractCodexCWDFromJSONLine(line []byte) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return ""
	}

	keys := []string{"cwd", "workdir", "working_dir", "directory", "path"}
	for _, key := range keys {
		if val := decodeJSONStringField(raw, key); val != "" {
			return val
		}
	}

	if payloadRaw, ok := raw["payload"]; ok && len(payloadRaw) > 0 {
		var payloadObj map[string]json.RawMessage
		if err := json.Unmarshal(payloadRaw, &payloadObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(payloadObj, key); val != "" {
					return val
				}
			}
		}
	}
	if contextRaw, ok := raw["context"]; ok && len(contextRaw) > 0 {
		var contextObj map[string]json.RawMessage
		if err := json.Unmarshal(contextRaw, &contextObj); err == nil {
			for _, key := range keys {
				if val := decodeJSONStringField(contextObj, key); val != "" {
					return val
				}
			}
		}
	}

	return ""
}

func decodeJSONStringField(raw map[string]json.RawMessage, key string) string {
	value, ok := raw[key]
	if !ok || len(value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// collectOtherCodexSessionIDs enumerates other managed tmux sessions and returns
// the CODEX_SESSION_ID values they currently own.
func (i *Instance) collectOtherCodexSessionIDs() map[string]bool {
	return i.codexExclusions(nil)
}

// shouldScanCodexSession returns whether we should run an expensive filesystem
// scan for Codex session rotation right now.
func (i *Instance) shouldScanCodexSession(allowUnscoped bool) bool {
	interval := codexRotationScanInterval
	if allowUnscoped {
		interval = codexBootstrapScanInterval
	}

	if !i.lastCodexScanAt.IsZero() && time.Since(i.lastCodexScanAt) < interval {
		return false
	}

	i.lastCodexScanAt = time.Now()
	return true
}

// shouldRunCodexProcessProbe returns whether we should run Codex process/file
// probing right now.
func (i *Instance) shouldRunCodexProcessProbe(force bool) bool {
	if force {
		i.lastCodexProbeAt = time.Now()
		return true
	}

	// Fast cadence only while bootstrapping (no session ID yet) — that phase is
	// bounded by discovery succeeding within seconds. Once an ID is known the
	// probe is just a rotation safety net behind the notify hook and tmux env,
	// so it backs off to the rotation cadence (issue #1552).
	interval := codexBootstrapScanInterval
	if i.CodexSessionID != "" {
		interval = codexRotationScanInterval
	}

	if !i.lastCodexProbeAt.IsZero() && time.Since(i.lastCodexProbeAt) < interval {
		return false
	}

	i.lastCodexProbeAt = time.Now()
	return true
}

// PaneProcessTreePIDs exports collectTmuxPaneProcessTreePIDs for callers
// outside internal/session — the #2100 send path, which uses pane-tree
// membership to decide which of several Claude session records addresses
// THIS session. Same contract as the unexported method: a non-nil error
// means the forest may be incomplete.
func (i *Instance) PaneProcessTreePIDs() ([]int, error) {
	return i.collectTmuxPaneProcessTreePIDs(nil)
}

// collectTmuxPaneProcessTreePIDs returns every pane PID and descendant. A
// non-nil error means the returned process forest may be incomplete. Its
// error strings are prefixed "codex process probe" for historical reasons
// (its first caller); it is not codex-specific — see PaneProcessTreePIDs.
// A status pass shares one process-table snapshot across its rows; nil reads
// a fresh one.
func (i *Instance) collectTmuxPaneProcessTreePIDs(pass *StatusUpdatePass) ([]int, error) {
	if i.tmuxSession == nil || !i.tmuxSession.Exists() {
		return nil, errors.New("codex process probe: tmux session is unavailable")
	}

	target := i.tmuxSession.Name
	// Target the same tmux server the session was created on (issue #687).
	// A session on an isolated agent-deck socket would return no panes from
	// the default server and we would mistakenly treat it as empty.
	// Bounded — see tmux.OutputBounded. Exec(...).Output() has no deadline, and
	// a tmux client that has exhausted its fd table never exits, so this probe
	// would hang the caller forever. -s makes list-panes cover every window in
	// the session rather than only the target's current window.
	out, err := tmux.OutputBounded(i.TmuxSocketName, "list-panes", "-s", "-t", target, "-F", "#{pane_pid}")
	if err != nil {
		return nil, fmt.Errorf("codex process probe: list tmux panes: %w", err)
	}
	panePIDs, paneErr := parsePositivePIDs(out, "tmux pane")
	if len(panePIDs) == 0 {
		return nil, paneErr
	}

	// Single snapshot of the process table is substantially cheaper than
	// spawning pgrep once per node in deep process trees.
	treePIDs, psErr := pass.processTree(panePIDs)
	if psErr == nil {
		return treePIDs, paneErr
	}

	// Fallback path for environments where ps output is unavailable/unexpected.
	var (
		allPIDs  []int
		pgrepErr error
	)
	seen := make(map[int]bool)
	for _, panePID := range panePIDs {
		pids, err := collectProcessTreePIDsViaPgrep(panePID)
		pgrepErr = errors.Join(pgrepErr, err)
		for _, pid := range pids {
			if !seen[pid] {
				seen[pid] = true
				allPIDs = append(allPIDs, pid)
			}
		}
	}
	if pgrepErr != nil {
		pgrepErr = errors.Join(fmt.Errorf("codex process probe: ps: %w", psErr), pgrepErr)
	}
	return allPIDs, errors.Join(paneErr, pgrepErr)
}

func parsePositivePIDs(output []byte, source string) ([]int, error) {
	var (
		pids     []int
		parseErr error
	)
	seen := make(map[int]bool)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		pid, err := strconv.Atoi(text)
		if err != nil || pid <= 0 {
			parseErr = errors.Join(parseErr, fmt.Errorf("codex process probe: invalid %s pid on line %d: %q", source, line, text))
			continue
		}
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	parseErr = errors.Join(parseErr, scanner.Err())
	if len(pids) == 0 && parseErr == nil {
		parseErr = fmt.Errorf("codex process probe: %s output contained no pids", source)
	}
	return pids, parseErr
}

func collectProcessTreePIDsFromTable(rootPIDs []int, procTable []byte) ([]int, error) {
	childrenByParent, parseErr := parsePSParentChildMap(procTable)
	return walkProcessTree(rootPIDs, childrenByParent, parseErr)
}

// walkProcessTree returns rootPIDs and their descendants in childrenByParent.
// parseErr (the table's own) is carried into the returned error.
func walkProcessTree(rootPIDs []int, childrenByParent map[int][]int, parseErr error) ([]int, error) {
	return walkProcessTreeIn(rootPIDs, childrenByParent, presentPIDs(childrenByParent), parseErr)
}

// presentPIDs returns every pid that childrenByParent lists as a child, i.e.
// every row of the ps table it was parsed from.
func presentPIDs(childrenByParent map[int][]int) map[int]bool {
	present := make(map[int]bool)
	for _, children := range childrenByParent {
		for _, pid := range children {
			present[pid] = true
		}
	}
	return present
}

// walkProcessTreeIn is walkProcessTree with present (presentPIDs of
// childrenByParent) already built, for a table shared by many walks.
func walkProcessTreeIn(rootPIDs []int, childrenByParent map[int][]int, present map[int]bool, parseErr error) ([]int, error) {
	var allPIDs []int
	seen := make(map[int]bool)
	queue := make([]int, 0, len(rootPIDs))
	for _, rootPID := range rootPIDs {
		if !present[rootPID] {
			parseErr = errors.Join(parseErr, fmt.Errorf("codex process probe: pane pid %d missing from ps process table", rootPID))
			continue
		}
		if !seen[rootPID] {
			seen[rootPID] = true
			queue = append(queue, rootPID)
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		allPIDs = append(allPIDs, parent)

		for _, childPID := range childrenByParent[parent] {
			if childPID <= 0 || seen[childPID] {
				continue
			}
			seen[childPID] = true
			queue = append(queue, childPID)
		}
	}
	return allPIDs, parseErr
}

func parsePSParentChildMap(procTable []byte) (map[int][]int, error) {
	childrenByParent := make(map[int][]int)
	var parseErr error
	scanner := bufio.NewScanner(bytes.NewReader(procTable))
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		fields := strings.Fields(text)
		// Tolerant of TRAILING columns so one snapshot can also carry comm=
		// for callers that identify the pane leader (parsePSCommandNames).
		// A row with fewer than two fields is genuinely malformed, and #1687's
		// fail-closed intent is kept by recording it rather than skipping it.
		if len(fields) < 2 {
			parseErr = errors.Join(parseErr, fmt.Errorf("codex process probe: invalid ps row %d: %q", line, text))
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			parseErr = errors.Join(parseErr, fmt.Errorf("codex process probe: invalid ps pid on row %d: %q", line, fields[0]))
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			parseErr = errors.Join(parseErr, fmt.Errorf("codex process probe: invalid ps parent pid on row %d: %q", line, fields[1]))
			continue
		}
		childrenByParent[ppid] = append(childrenByParent[ppid], pid)
	}
	return childrenByParent, errors.Join(parseErr, scanner.Err())
}

// parsePSCommandNames reads `pid=,ppid=,comm=` output into pid -> command
// name. Comm is the last column and may contain spaces, so everything from
// the third field on is joined. macOS reports comm as an absolute path while
// Linux reports the bare name, so the result is reduced to the base name to
// give callers one shape to compare against.
func parsePSCommandNames(procTable []byte) map[int]string {
	commByPID := make(map[int]string)
	scanner := bufio.NewScanner(bytes.NewReader(procTable))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		commByPID[pid] = filepath.Base(strings.Join(fields[2:], " "))
	}
	return commByPID
}

func collectProcessTreePIDsViaPgrep(rootPID int) ([]int, error) {
	var (
		allPIDs  []int
		probeErr error
	)
	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		allPIDs = append(allPIDs, parent)

		// #nosec G204 -- "pgrep" is a fixed binary name and the only argument is
		// strconv.Itoa(int), never reachable from external input.
		childrenRaw, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
				continue // pgrep uses exit 1 when the process has no children.
			}
			probeErr = errors.Join(probeErr, fmt.Errorf("codex process probe: pgrep children of %d: %w", parent, err))
			continue
		}
		children, parseErr := parsePositivePIDs(childrenRaw, "pgrep")
		probeErr = errors.Join(probeErr, parseErr)
		for _, childPID := range children {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			queue = append(queue, childPID)
		}
	}
	return allPIDs, probeErr
}

func isLikelyCodexProcessPID(pid int) (bool, error) {
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	argsOut, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false, fmt.Errorf("codex process probe: inspect pid %d: %w", pid, err)
	}
	return isCodexArgs(string(argsOut)), nil
}

func isCodexArgs(args string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(args)), "codex")
}

func (i *Instance) collectCodexProcessCandidates(pass *StatusUpdatePass) ([]int, error) {
	pids, probeErr := i.collectTmuxPaneProcessTreePIDs(pass)
	candidates := make([]int, 0, len(pids))
	for _, pid := range pids {
		likely, err := pass.isLikelyCodexProcess(pid)
		if err != nil {
			probeErr = errors.Join(probeErr, err)
			continue
		}
		if likely {
			candidates = append(candidates, pid)
		}
	}
	return candidates, probeErr
}

func extractCodexSessionIDFromPath(path string) string {
	normalized := strings.TrimSpace(path)
	normalized = strings.TrimSuffix(normalized, " (deleted)")
	matches := codexSessionIDPathPatternRE.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func extractCodexSessionIDFromLsofOutput(output []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		if sessionID := extractCodexSessionIDFromPath(scanner.Text()); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func (i *Instance) extractAcceptedCodexSessionIDFromOutput(output []byte) string {
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		candidateID := extractCodexSessionIDFromPath(scanner.Text())
		if candidateID == "" || seen[candidateID] {
			continue
		}
		seen[candidateID] = true
		if sessionID := i.filterCodexProcessProbeCandidate(candidateID); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func (i *Instance) extractCodexSessionIDFromProcFD(pid int) (string, error) {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return "", fmt.Errorf("codex process probe: read %s: %w", fdDir, err)
	}

	var probeErr error
	for _, entry := range entries {
		targetPath := filepath.Join(fdDir, entry.Name())
		target, err := os.Readlink(targetPath)
		if err != nil {
			probeErr = errors.Join(probeErr, fmt.Errorf("codex process probe: readlink %s: %w", targetPath, err))
			continue
		}
		if candidateID := extractCodexSessionIDFromPath(target); candidateID != "" {
			if sessionID := i.filterCodexProcessProbeCandidate(candidateID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	return "", probeErr
}

func (i *Instance) queryCodexSessionFromHostProcFD(pass *StatusUpdatePass) (string, error) {
	candidates, probeErr := i.collectCodexProcessCandidates(pass)
	for _, pid := range candidates {
		sessionID, err := i.extractCodexSessionIDFromProcFD(pid)
		if sessionID != "" {
			return sessionID, nil
		}
		probeErr = errors.Join(probeErr, err)
	}
	return "", probeErr
}

func codexProcFDProbeScript(procRoot string) string {
	root := shellescape.Quote(strings.TrimRight(procRoot, "/"))
	return fmt.Sprintf(
		`command -v readlink >/dev/null 2>&1 || {
	echo %q
	exit 0
}
proc_root=%s
saw_process=0
incomplete=0
for p in "$proc_root"/[0-9]*; do
	if [ "$p" = "$proc_root/[0-9]*" ]; then
		break
	fi
	saw_process=1
	if [ ! -d "$p" ]; then
		incomplete=1
		continue
	fi
	d="$p/fd"
	if [ ! -d "$d" ] || [ ! -r "$d" ] || [ ! -x "$d" ]; then
		incomplete=1
		continue
	fi
	for f in "$d"/*; do
		if [ "$f" = "$d/*" ]; then
			incomplete=1
			break
		fi
		if ! t=$(readlink "$f" 2>/dev/null); then
			incomplete=1
			continue
		fi
		case "$t" in
			*/sessions/*rollout-*.jsonl*)
				printf '%%s\n' "$t"
				;;
		esac
	done
done
if [ "$saw_process" -eq 0 ]; then
	incomplete=1
fi
if [ "$incomplete" -ne 0 ]; then
	echo %q
fi`,
		codexProbeMissingSentinel,
		root,
		codexProbeIncompleteSentinel,
	)
}

func (i *Instance) queryCodexSessionFromDockerProcFD() (string, string, error) {
	if strings.TrimSpace(i.SandboxContainer) == "" {
		return "", "", errors.New("codex process probe: sandbox container is unavailable")
	}

	script := codexProcFDProbeScript("/proc")
	// #nosec G204 -- "docker exec" with internal SandboxContainer name and a
	// hardcoded shell probe script (the sentinels are compile-time constants);
	// no external input flows here.
	out, err := exec.Command("docker", "exec", i.SandboxContainer, "sh", "-lc", script).Output()
	if sessionID := i.extractAcceptedCodexSessionIDFromOutput(out); sessionID != "" {
		return sessionID, "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("codex process probe: docker exec: %w", err)
	}
	if bytes.Contains(out, []byte(codexProbeMissingSentinel)) {
		return "", "readlink", errors.New("codex process probe: readlink is unavailable")
	}
	if bytes.Contains(out, []byte(codexProbeIncompleteSentinel)) {
		return "", "", errors.New("codex process probe: container fd scan is incomplete")
	}
	return "", "", nil
}

// procfdOpenVnodePaths is a test seam over procfd.OpenVnodePaths so the
// native-probe semantics (ID extraction, answered/lsof-fallback logic) stay
// testable on platforms where the real scan is unsupported.
var procfdOpenVnodePaths = procfd.OpenVnodePaths

// queryCodexSessionFromHostNative probes candidate PIDs with the in-process
// libproc FD scan (issue #1552: spawning lsof per PID on macOS walks every FD
// and resolves every vnode path, and polling it stalled whole machines). A
// non-nil error means the negative result is incomplete and requires fallback.
func (i *Instance) queryCodexSessionFromHostNative(pids []int) (string, error) {
	var probeErr error
	for _, pid := range pids {
		paths, err := procfdOpenVnodePaths(pid)
		for _, path := range paths {
			candidateID := extractCodexSessionIDFromPath(path)
			if sessionID := i.filterCodexProcessProbeCandidate(candidateID); sessionID != "" {
				return sessionID, nil
			}
		}
		if err != nil {
			if !errors.Is(err, procfd.ErrUnsupported) {
				sessionLog.Debug("codex_procfd_probe_failed", slog.Int("pid", pid), slog.Any("error", err))
			}
			probeErr = errors.Join(probeErr, fmt.Errorf("codex native probe pid %d: %w", pid, err))
		}
	}
	return "", probeErr
}

func (i *Instance) queryCodexSessionFromHostLsof(pids []int) (string, string, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return "", "lsof", fmt.Errorf("codex process probe: find lsof: %w", err)
	}

	var probeErr error
	for _, pid := range pids {
		// -n -P disables reverse-DNS host and port-name resolution so a resolver
		// that drops PTR queries cannot stall the probe (issue #1581); the
		// context timeout bounds the call even if lsof hangs for any other reason.
		ctx, cancel := context.WithTimeout(context.Background(), codexLsofProbeTimeout)
		// #nosec G204 -- "lsof" is a fixed binary; only dynamic arg is strconv.Itoa(int).
		out, err := exec.CommandContext(ctx, "lsof", "-n", "-P", "-p", strconv.Itoa(pid)).Output()
		cancel()
		if sessionID := i.extractAcceptedCodexSessionIDFromOutput(out); sessionID != "" {
			return sessionID, "", nil
		}
		if err != nil {
			var execErr *exec.Error
			if errors.As(err, &execErr) && execErr.Err == exec.ErrNotFound {
				return "", "lsof", fmt.Errorf("codex process probe: run lsof: %w", err)
			}
			sessionLog.Debug("codex_lsof_probe_failed", slog.Int("pid", pid), slog.Any("error", err))
			probeErr = errors.Join(probeErr, fmt.Errorf("codex process probe: lsof pid %d: %w", pid, err))
			continue
		}
	}

	return "", "", probeErr
}

// queryCodexSessionFromProcessFiles inspects live Codex processes and returns
// the active session UUID inferred from open rollout JSONL files. Empty ID with
// nil error means definitely absent; a non-nil error means undetermined. The
// second return value names a missing dependency, when applicable.
func (i *Instance) queryCodexSessionFromProcessFiles(pass *StatusUpdatePass) (string, string, error) {
	// Sandboxed sessions run Codex inside Docker; probe container /proc.
	if i.IsSandboxed() {
		return i.queryCodexSessionFromDockerProcFD()
	}

	// Linux/WSL: pure in-process /proc scanning (no lsof dependency).
	if runtime.GOOS == "linux" {
		sessionID, err := i.queryCodexSessionFromHostProcFD(pass)
		return sessionID, "", err
	}

	// Non-Linux (e.g. macOS): in-process libproc FD scan first, with lsof kept
	// only as a compatibility fallback when the native probe cannot answer.
	candidates, candidateErr := i.collectCodexProcessCandidates(pass)
	if sessionID, nativeErr := i.queryCodexSessionFromHostNative(candidates); sessionID != "" {
		return sessionID, "", nil
	} else if nativeErr == nil && candidateErr == nil {
		return "", "", nil
	}

	sessionID, missingDep, lsofErr := i.queryCodexSessionFromHostLsof(candidates)
	if sessionID != "" {
		return sessionID, "", nil
	}
	return "", missingDep, errors.Join(candidateErr, lsofErr)
}

// ConsumeCodexRestartWarning returns and clears any pending Codex restart warning.
func (i *Instance) ConsumeCodexRestartWarning() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	warning := strings.TrimSpace(i.pendingCodexRestartWarning)
	i.pendingCodexRestartWarning = ""
	return warning
}

func codexProbeMissingWarning(missingDep string) string {
	missingDep = strings.TrimSpace(missingDep)
	if missingDep == "" {
		return ""
	}
	return fmt.Sprintf("Codex session detection fallback: %s is not available", missingDep)
}

// UpdateCodexSession updates the Codex session ID.
// Primary source: tmux environment.
// Fallback: project-aware filesystem scan.
func (i *Instance) UpdateCodexSession(excludeIDs map[string]bool) {
	// Preserve the explicit caller-supplied exclusion API: nil means no exclusions.
	// Status passes use the lazy ownership lookup through updateCodexSessionForPass.
	if excludeIDs == nil {
		excludeIDs = make(map[string]bool)
	}
	i.updateCodexSession(excludeIDs, false)
}

// updateCodexSession refreshes Codex session ID from env/process-files/disk.
// Returns missing dependency name when probe prerequisites are unavailable.
func (i *Instance) updateCodexSession(excludeIDs map[string]bool, forceProbe bool) string {
	return i.updateCodexSessionForPass(excludeIDs, forceProbe, nil, false)
}

// statusLocked is true only when called from updateStatus with i.mu held.
func (i *Instance) updateCodexSessionForPass(excludeIDs map[string]bool, forceProbe bool, pass *StatusUpdatePass, statusLocked bool) string {
	if !IsCodexCompatible(i.Tool) {
		return ""
	}

	envSessionID := ""

	// 1. Try to read from tmux environment first (authoritative if set)
	if i.tmuxSession != nil {
		if sessionID, err := i.tmuxSession.GetEnvironment("CODEX_SESSION_ID"); err == nil && sessionID != "" {
			envSessionID = sessionID
			if i.CodexSessionID != sessionID {
				i.CodexSessionID = sessionID
			}
			i.CodexDetectedAt = time.Now()
			i.recordCodexOwnership(i.CodexSessionID)
		}
	}

	// 2. Prefer live-process file detection (Linux /proc, macOS lsof fallback).
	missingProbeDep := ""
	if i.shouldRunCodexProcessProbe(forceProbe) {
		sessionID, missingDep, probeErr := i.queryCodexSessionFromProcessFiles(pass)
		sessionID = i.filterCodexProcessProbeCandidate(sessionID)
		if sessionID != "" {
			changed := sessionID != i.CodexSessionID
			if changed {
				sessionLog.Debug(
					"codex_session_update_from_probe",
					slog.String("old_id", i.CodexSessionID),
					slog.String("new_id", sessionID),
				)
			}
			i.CodexSessionID = sessionID
			i.CodexDetectedAt = time.Now()
			i.recordCodexOwnership(i.CodexSessionID)
			if i.tmuxSession != nil && i.tmuxSession.Exists() && (changed || envSessionID == "") {
				_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
			}
			return ""
		}
		if missingDep != "" {
			missingProbeDep = missingDep
		}
		if probeErr != nil {
			sessionLog.Debug("codex_session_probe_undetermined", slog.Any("error", probeErr))
			return missingProbeDep
		}
	}

	// 3. Use disk scan only as a bootstrap fallback. Once a session ID is
	// known, rotation must come from authoritative live evidence above (tmux
	// env, hook payload, or the Codex process' open rollout file). Polling the
	// full historical $CODEX_HOME/sessions tree for every active Codex session
	// burns CPU on large histories and has no stronger ownership signal than the
	// current binding.
	if i.CodexSessionID != "" {
		return missingProbeDep
	}

	// Only allow unscoped fallback when we don't have a known session ID yet.
	allowUnscoped := envSessionID == "" && i.CodexSessionID == "" && i.CodexStartedAt > 0
	if !i.shouldScanCodexSession(allowUnscoped) {
		return missingProbeDep
	}
	if threadID, live := i.liveCodexBootstrapEvidence(pass); live {
		if threadID != "" {
			i.CodexSessionID = threadID
			i.CodexDetectedAt = time.Now()
			i.recordCodexOwnership(threadID)
			if i.tmuxSession != nil && i.tmuxSession.Exists() {
				_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", threadID)
			}
		}
		return missingProbeDep
	}

	if statusLocked {
		return i.bootstrapCodexSessionStatusLocked(excludeIDs, pass, allowUnscoped, envSessionID, missingProbeDep)
	}

	// Fetch peer ownership before taking the bootstrap selection lock. Status
	// readers and authoritative hook updates must remain available during I/O.
	if excludeIDs == nil {
		if pass == nil {
			pass = &StatusUpdatePass{}
		}
		socket := tmux.DefaultSocketName()
		if i.tmuxSession != nil {
			socket = i.tmuxSession.SocketName
		}
		pass.codexOwnership(socket)
	}

	// Serialize fallback selection with publication so peers sharing a project
	// cannot claim the same rollout from a pinned ownership snapshot.
	codexBootstrapMu.Lock()
	defer codexBootstrapMu.Unlock()
	// Collect peer ownership only when a bootstrap disk scan will actually run.
	if excludeIDs == nil {
		excludeIDs = i.codexExclusions(pass)
		if excludeIDs == nil {
			return missingProbeDep // Unknown ownership must not become a guessed binding.
		}
	}

	if sessionID := i.queryCodexSession(excludeIDs, allowUnscoped); sessionID != "" {
		i.publishCodexBootstrapSession(sessionID, envSessionID)
	}
	return missingProbeDep
}

// bootstrapCodexSessionStatusLocked runs the bootstrap disk scan for the
// status pass. It is entered and returns with i.mu held, but releases i.mu
// around the ownership refresh and the $CODEX_HOME walk: holding it there
// stalled every render-path reader for up to codexWalkDirTimeout (#2674).
// Lock order is codexBootstrapMu before i.mu.
func (i *Instance) bootstrapCodexSessionStatusLocked(excludeIDs map[string]bool, pass *StatusUpdatePass, allowUnscoped bool, envSessionID, missingProbeDep string) string {
	if pass == nil {
		pass = &StatusUpdatePass{}
	}
	socket := tmux.DefaultSocketName()
	ts := i.tmuxSession
	started := i.lastStartTime
	hookID := i.hookSessionID
	if ts != nil {
		socket = ts.SocketName
	}
	// A stop, restart, or authoritative binding while unlocked supersedes
	// this bootstrap attempt. Do not resurrect or overwrite it.
	superseded := func() bool {
		return i.tmuxSession != ts || i.lastStartTime != started || i.hookSessionID != hookID ||
			i.CodexSessionID != "" || !IsCodexCompatible(i.Tool) ||
			(i.Status != StatusRunning && i.Status != StatusWaiting)
	}

	i.mu.Unlock()
	if excludeIDs == nil {
		pass.codexOwnership(socket)
	}
	codexBootstrapMu.Lock()
	defer codexBootstrapMu.Unlock() // runs after return, with i.mu re-held
	i.mu.Lock()
	if superseded() {
		return missingProbeDep
	}
	if excludeIDs == nil {
		excludeIDs = i.codexExclusions(pass) // ownership is warm in pass
		if excludeIDs == nil {
			return missingProbeDep // Unknown ownership must not become a guessed binding.
		}
	}
	codexHome, projectPath, startedAt, id := i.getCodexHomeDir(), i.ProjectPath, i.CodexStartedAt, i.ID

	i.mu.Unlock()
	sessionID := queryCodexSessionWith(codexHome, projectPath, startedAt, id, excludeIDs, allowUnscoped)
	i.mu.Lock()

	if sessionID != "" && !superseded() {
		i.publishCodexBootstrapSession(sessionID, envSessionID)
	}
	return missingProbeDep
}

// publishCodexBootstrapSession binds a disk-scan result. Callers hold
// codexBootstrapMu and, when called from the status pass, i.mu.
func (i *Instance) publishCodexBootstrapSession(sessionID, envSessionID string) {
	// queryCodexSession already filters subagent rollouts out of candidacy
	// (incident 2026-07-15), so sessionID here is always a user thread.
	changed := sessionID != i.CodexSessionID
	if changed {
		sessionLog.Debug(
			"codex_session_update",
			slog.String("old_id", i.CodexSessionID),
			slog.String("new_id", sessionID),
		)
	}
	i.CodexSessionID = sessionID
	i.CodexDetectedAt = time.Now()
	i.recordCodexOwnership(i.CodexSessionID)

	// Sync back to tmux environment for future restarts
	// Skip redundant writes when env already matches: each write is a tmux subprocess.
	if i.tmuxSession != nil && i.tmuxSession.Exists() && (changed || envSessionID == "") {
		_ = i.tmuxSession.SetEnvironment("CODEX_SESSION_ID", i.CodexSessionID)
	}
}
