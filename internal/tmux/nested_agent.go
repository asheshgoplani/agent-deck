package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// Nested-agent detection walks the tmux pane's process tree instead of reading
// pane text. Screen keywords are not evidence: an editor buffer or a conversation
// can name every vendor at once. A match is an exact executable basename, or a
// known interpreter/launcher whose entrypoint argument is an exact known script
// or package name.
//
// The outermost agent ancestor wins. A descendant the agent spawned for a tool
// call — even one whose executable is itself an agent — does not replace it.
// Two agents where neither is an ancestor of the other are ambiguous siblings
// (two Neovim terminals, one Claude and one Gemini); that fails closed.
//
// The chosen process is bound to its start identity so a later sample can tell
// "still the same process" from "PID reused" or "already exited". Configured
// launch identity is not this observation: callers must not restart from it.

// agentExecutables maps an exact executable basename to a tool id. "agent" is
// Cursor's standalone binary; it is not an interpreter entrypoint, because a
// script named agent.js is not evidence of Cursor.
var agentExecutables = map[string]string{
	"claude":    "claude",
	"gemini":    "gemini",
	"opencode":  "opencode",
	"open-code": "opencode",
	"codex":     "codex",
	"copilot":   "copilot",
	"crush":     "crush",
	"muse":      "muse",
	"cursor":    "cursor",
	"agent":     "cursor",
	"hermes":    "hermes",
	"dsh":       "deepseek",
	"pi":        "pi",
	"omp":       "omp",
	"oh-my-pi":  "omp",
}

// interpreterEntrypoints is the same set minus the ambiguous "agent" basename.
var interpreterEntrypoints = map[string]string{
	"claude":    "claude",
	"gemini":    "gemini",
	"opencode":  "opencode",
	"open-code": "opencode",
	"codex":     "codex",
	"copilot":   "copilot",
	"crush":     "crush",
	"muse":      "muse",
	"cursor":    "cursor",
	"hermes":    "hermes",
	"dsh":       "deepseek",
	"pi":        "pi",
	"omp":       "omp",
	"oh-my-pi":  "omp",
}

// knownInterpreters are processes whose own name is not the agent. The
// entrypoint argument has to name the agent exactly.
var knownInterpreters = map[string]bool{
	"node": true, "nodejs": true, "bun": true, "deno": true,
	"python": true, "python3": true, "ruby": true, "perl": true,
}

// knownLaunchers wrap an exact agent executable or a known package spec.
// They are not evidence by themselves.
var knownLaunchers = map[string]bool{
	"npx": true, "bunx": true, "env": true,
}

// inlineCodeFlags mean the following text is a program, not an entrypoint
// path. Parsing it would treat "echo claude" as a runtime.
var inlineCodeFlags = map[string]bool{
	"-e": true, "--eval": true, "-c": true,
}

// flagTakesValue are interpreter/launcher flags whose next token is an
// argument, not the entrypoint. -m is handled separately: its value is a
// module name, which can itself be an entrypoint.
var flagTakesValue = map[string]bool{
	"-r": true, "--require": true, "--loader": true, "--experimental-loader": true,
	"--import": true, "-C": true, "--directory": true,
}

const ompPackageSpec = "@oh-my-pi/pi-coding-agent"

// processSample is one row of a pane process snapshot.
type processSample struct {
	PID     int
	PPID    int
	Comm    string
	Argv    []string
	StartID string
	Zombie  bool
}

// nestedAgentMatch is the single outermost agent in a pane tree.
type nestedAgentMatch struct {
	Tool    string
	PID     int
	StartID string
}

// PaneProcess is the test-facing form of a process-tree row.
type PaneProcess struct {
	PID     int
	PPID    int
	Comm    string
	Argv    []string
	StartID string
	Zombie  bool
}

// agentTreeStatus is the outcome of one pane-tree classification.
// Unavailable means the snapshot could not be rooted at the pane PID.
// None means the tree was readable and contained no agent. Ambiguous means
// two or more outermost agents, so a foreground name must not be used to guess.
type agentTreeStatus int

const (
	agentTreeUnavailable agentTreeStatus = iota
	agentTreeNone
	agentTreeAmbiguous
	agentTreeMatch
)

// detectNestedAgent classifies procs as the tree rooted at panePID.
// Screen text is not an input.
func detectNestedAgent(panePID int, procs []processSample) (nestedAgentMatch, agentTreeStatus) {
	if panePID <= 0 {
		return nestedAgentMatch{}, agentTreeUnavailable
	}
	byPID := make(map[int]processSample, len(procs))
	children := make(map[int][]int, len(procs))
	for _, proc := range procs {
		if proc.PID <= 0 || proc.Zombie {
			continue
		}
		byPID[proc.PID] = proc
		children[proc.PPID] = append(children[proc.PPID], proc.PID)
	}
	if _, ok := byPID[panePID]; !ok {
		return nestedAgentMatch{}, agentTreeUnavailable
	}

	parent := make(map[int]int)
	var agents []nestedAgentMatch
	queue := []int{panePID}
	seen := map[int]bool{panePID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if tool, ok := classifyProcess(byPID[pid]); ok {
			agents = append(agents, nestedAgentMatch{
				Tool:    tool,
				PID:     pid,
				StartID: byPID[pid].StartID,
			})
		}
		for _, child := range children[pid] {
			if seen[child] {
				continue
			}
			if _, ok := byPID[child]; !ok {
				continue
			}
			seen[child] = true
			parent[child] = pid
			queue = append(queue, child)
		}
	}

	agentPIDs := make(map[int]struct{}, len(agents))
	for _, agent := range agents {
		agentPIDs[agent.PID] = struct{}{}
	}
	var outermost []nestedAgentMatch
	for _, agent := range agents {
		hasAgentAncestor := false
		for cur := parent[agent.PID]; cur != 0; cur = parent[cur] {
			if _, ok := agentPIDs[cur]; ok {
				hasAgentAncestor = true
				break
			}
		}
		if !hasAgentAncestor {
			outermost = append(outermost, agent)
		}
	}
	switch len(outermost) {
	case 0:
		return nestedAgentMatch{}, agentTreeNone
	case 1:
		return outermost[0], agentTreeMatch
	default:
		// Siblings or cousins. Guessing would persist the wrong identity.
		return nestedAgentMatch{}, agentTreeAmbiguous
	}
}

// classifyProcess recognizes exact agent executables or interpreter entrypoints.
func classifyProcess(proc processSample) (string, bool) {
	base := normalizeExe(proc.Comm)
	if tool, ok := agentExecutables[base]; ok {
		return tool, true
	}
	if !knownInterpreters[base] && !knownLaunchers[base] {
		return "", false
	}
	if len(proc.Argv) == 0 {
		return "", false
	}
	return classifyInterpreterArgv(base, proc.Argv)
}

// classifyInterpreterArgv skips launcher options without treating inline code as an agent.
func classifyInterpreterArgv(comm string, argv []string) (string, bool) {
	start := 0
	if len(argv) > 0 && normalizeExe(argv[0]) == comm {
		start = 1
	}
	for i := start; i < len(argv); i++ {
		arg := strings.Trim(argv[i], `"'`)
		if arg == "" || isShellAssignmentToken(arg) {
			continue
		}
		if arg == "--" {
			if i+1 >= len(argv) {
				return "", false
			}
			return entrypointTool(argv[i+1]), entrypointTool(argv[i+1]) != ""
		}
		if !strings.HasPrefix(arg, "-") {
			if comm == "env" {
				// env's command-position word is the real executable, which
				// may itself be an interpreter. Recurse on the tail.
				return classifyProcess(processSample{Comm: arg, Argv: argv[i:]})
			}
			if tool := entrypointTool(arg); tool != "" {
				return tool, true
			}
			return "", false
		}
		name := arg
		if eq := strings.IndexByte(arg, '='); eq > 0 && strings.HasPrefix(arg, "--") {
			name = arg[:eq]
		}
		if inlineCodeFlags[name] {
			return "", false
		}
		if name == "-m" || name == "--module" {
			if i+1 >= len(argv) {
				return "", false
			}
			module := strings.Trim(argv[i+1], `"'`)
			if tool, ok := interpreterEntrypoints[normalizeExe(module)]; ok && !strings.Contains(module, ".") {
				return tool, true
			}
			return "", false
		}
		if flagTakesValue[name] && !strings.Contains(arg, "=") {
			i++
		}
	}
	return "", false
}

// entrypointTool resolves known packages and script basenames, never arbitrary suffixes.
func entrypointTool(arg string) string {
	trimmed := strings.Trim(arg, `"'`)
	if trimmed == ompPackageSpec {
		return "omp"
	}
	base := normalizeExe(trimmed)
	// Strip a single script extension so node .../opencode.js still matches,
	// but do not peel arbitrary suffixes (claude-deck stays claude-deck).
	switch ext := filepath.Ext(base); ext {
	case ".js", ".mjs", ".cjs", ".ts", ".py", ".rb":
		base = strings.TrimSuffix(base, ext)
	}
	if tool, ok := interpreterEntrypoints[base]; ok {
		return tool
	}
	return ""
}

// normalizeExe strips path, quoting, case, and Windows launcher extensions.
func normalizeExe(path string) string {
	base := filepath.Base(strings.Trim(strings.TrimSpace(path), `"'`))
	base = strings.ToLower(base)
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	return base
}

// isInterpreterOrLauncher reports whether argv must be inspected for tool identity.
func isInterpreterOrLauncher(base string) bool {
	return knownInterpreters[base] || knownLaunchers[base]
}

// agentTreeOverride, when set, replaces the live pane probe for this session.
// Tests use it so detection does not depend on the caller's process table.
func (s *Session) agentTree() (int, []processSample, error) {
	if s != nil && s.agentTreeOverride != nil {
		return s.agentTreeOverride()
	}
	return readLivePaneAgentTree(s)
}

// observeNestedAgent binds an unambiguous pane-tree match to its process start identity.
func (s *Session) observeNestedAgent() (nestedAgentMatch, agentTreeStatus) {
	panePID, procs, err := s.agentTree()
	if err != nil || panePID <= 0 {
		return nestedAgentMatch{}, agentTreeUnavailable
	}
	match, status := detectNestedAgent(panePID, procs)
	if status != agentTreeMatch {
		return nestedAgentMatch{}, status
	}
	if match.StartID == "" {
		if id, idErr := processIdentityOf(context.Background(), match.PID); idErr == nil {
			match.StartID = id
		}
	}
	return match, agentTreeMatch
}

// agentIncarnationAlive reuses successful PID/start-identity probes for one
// second, independently of tool detection. Never use this cache to authorize
// message delivery: process exit or reuse can take up to one second to surface.
func (s *Session) agentIncarnationAlive(pid int, startID string) bool {
	if pid <= 0 || startID == "" {
		return false
	}
	s.agentIdentityMu.Lock()
	defer s.agentIdentityMu.Unlock()
	if s.agentIdentityPID == pid && s.agentIdentityStart == startID &&
		time.Since(s.agentIdentityCheckedAt) < time.Second {
		return true
	}
	s.agentIdentityCheckedAt = time.Time{}
	got, err := processIdentityOf(context.Background(), pid)
	if err != nil || got != startID {
		return false
	}
	s.agentIdentityPID = pid
	s.agentIdentityStart = startID
	s.agentIdentityCheckedAt = time.Now()
	return true
}

// readLivePaneAgentTree snapshots the process tree rooted at the tmux pane PID.
// A probe error is indeterminate: callers fail closed and do not consult pane text.
func readLivePaneAgentTree(s *Session) (int, []processSample, error) {
	if s == nil {
		return 0, nil, fmt.Errorf("no session")
	}
	panePID, err := s.PanePID()
	if err != nil {
		return 0, nil, err
	}
	// #nosec G204 -- "ps" is a fixed binary; the argument list is constant.
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,stat=,comm=").Output()
	if err != nil {
		return 0, nil, fmt.Errorf("ps process table: %w", err)
	}
	procs := parsePSTree(out)
	fillInterpreterArgv(panePID, procs)
	return panePID, procs, nil
}

// parsePSTree decodes fixed ps columns and marks exited processes for exclusion.
func parsePSTree(out []byte) []processSample {
	var procs []processSample
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			continue
		}
		stat := fields[2]
		zombie := stat != "" && (stat[0] == 'Z' || stat[0] == 'X')
		procs = append(procs, processSample{
			PID:    pid,
			PPID:   ppid,
			Comm:   strings.Join(fields[3:], " "),
			Zombie: zombie,
		})
	}
	return procs
}

// fillInterpreterArgv reads arguments only for interpreters in the rooted pane tree.
func fillInterpreterArgv(panePID int, procs []processSample) {
	index := make(map[int]int, len(procs))
	children := make(map[int][]int, len(procs))
	for i, proc := range procs {
		index[proc.PID] = i
		children[proc.PPID] = append(children[proc.PPID], proc.PID)
	}
	if _, ok := index[panePID]; !ok {
		return
	}
	queue := []int{panePID}
	seen := map[int]bool{panePID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		proc := &procs[index[pid]]
		if !proc.Zombie && isInterpreterOrLauncher(normalizeExe(proc.Comm)) {
			if argv, err := readProcArgv(pid); err == nil {
				proc.Argv = argv
			}
		}
		for _, child := range children[pid] {
			if seen[child] {
				continue
			}
			if _, ok := index[child]; !ok {
				continue
			}
			seen[child] = true
			queue = append(queue, child)
		}
	}
}

// readProcArgv prefers lossless procfs arguments, with a conservative ps fallback.
func readProcArgv(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		parts := bytes.Split(data, []byte{0})
		argv := make([]string, 0, len(parts))
		for _, part := range parts {
			if len(part) == 0 {
				continue
			}
			argv = append(argv, string(part))
		}
		if len(argv) > 0 {
			return argv, nil
		}
	}
	// #nosec G204 -- "ps" is a fixed binary; the only argument is a pid.
	out, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if psErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, psErr
	}
	// ps joins argv with spaces, so this is only a fallback when /proc/cmdline
	// is unavailable. An unquoted space inside an argument cannot be recovered;
	// that row then fails closed rather than guessing an identity from prose.
	argv, ok := shellwords.Split(strings.TrimSpace(string(out)))
	if !ok || len(argv) == 0 {
		return nil, fmt.Errorf("unparseable args for pid %d", pid)
	}
	return argv, nil
}
