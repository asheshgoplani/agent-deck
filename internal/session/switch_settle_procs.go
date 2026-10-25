package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// SettleProcReader is the process view settle needs: parent links and argv
// for every live process, and one environment variable of one process.
type SettleProcReader interface {
	Commands() (map[int]procowner.ProcCommand, error)
	Env(pid int, key string) string
}

// settleDescendants walks the parent links from root (root excluded).
func settleDescendants(procs SettleProcReader, root int) []int {
	cmds, err := procs.Commands()
	if err != nil || root <= 0 {
		return nil
	}
	children := map[int][]int{}
	for pid, cmd := range cmds {
		children[cmd.PPID] = append(children[cmd.PPID], pid)
	}
	var out []int
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 && len(out) < 4096 {
		next := queue[0]
		queue = queue[1:]
		for _, child := range children[next] {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
				queue = append(queue, child)
			}
		}
	}
	return out
}

// SnapshotPaneProcesses lists the session's pane pid and every process
// under it: the tree a restart replaces. Taken before a switch stops the
// session, so settle can wait until each of them, a plugin poller above all,
// is gone.
func SnapshotPaneProcesses(inst *Instance) []int {
	if inst == nil {
		return nil
	}
	tm := inst.GetTmuxSession()
	if tm == nil || !tm.Exists() {
		return nil
	}
	pid, err := tm.PrimaryPanePID()
	if err != nil || pid <= 0 {
		return nil
	}
	out := []int{pid}
	if procs := NewHostSettleProcReader(); procs != nil {
		out = append(out, settleDescendants(procs, pid)...)
	}
	return out
}

// liveOnAccount reports whether the session's own Claude is running on the
// account config dir accountDir: its pane exists, and every top-level Claude
// process under it carrying the session's id runs on that account (directly
// or through a worker-scratch mirror). It waits briefly, since the pane's
// shell needs a moment to exec Claude. Used to tell a start that failed from
// one that reported an error while the session came up anyway.
func liveOnAccount(inst *Instance, accountDir string) bool {
	if inst == nil || accountDir == "" {
		return false
	}
	procs := NewHostSettleProcReader()
	if procs == nil {
		return false
	}
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		pids := SnapshotPaneProcesses(inst)
		if len(pids) == 0 {
			continue
		}
		cmds, err := procs.Commands()
		if err != nil {
			return false
		}
		checked, agreed := 0, 0
		for _, pid := range pids {
			if procs.Env(pid, "AGENTDECK_INSTANCE_ID") != inst.ID || !settleClaudeCommand(cmds[pid].Args) || settleUnderClaude(cmds, pid, pids[0]) {
				continue
			}
			checked++
			if settleRunsOnAccount(procs.Env(pid, "CLAUDE_CONFIG_DIR"), accountDir) {
				agreed++
			}
		}
		if checked > 0 {
			return agreed == checked
		}
	}
	return false
}

// hostProcReader reads the live process table: /proc on Linux, ps on macOS.
type hostProcReader struct {
	cmds procowner.CommandReader
}

// NewHostSettleProcReader returns the production process reader, or nil
// when the platform cannot list processes.
func NewHostSettleProcReader() SettleProcReader {
	reader, ok := procowner.NewProber().(procowner.CommandReader)
	if !ok {
		return nil
	}
	return hostProcReader{cmds: reader}
}

func (h hostProcReader) Commands() (map[int]procowner.ProcCommand, error) {
	return h.cmds.Commands()
}

func (h hostProcReader) Env(pid int, key string) string {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			return ""
		}
		return envValue(bytes.Split(data, []byte{0}), key)
	}
	// macOS: ps -E appends the environment to the command for processes
	// of the same user. Values with spaces are ambiguous there, so only a
	// whitespace-free value is trusted.
	if pid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-Eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: fixed binary and flags, no shell; the only argument is a positive int pid
	if err != nil {
		return ""
	}
	return psEnvValue(string(out), key)
}

func envValue(entries [][]byte, key string) string {
	prefix := []byte(key + "=")
	for _, entry := range entries {
		if bytes.HasPrefix(entry, prefix) {
			return string(entry[len(prefix):])
		}
	}
	return ""
}

// psEnvValue finds " KEY=value" in a ps -E line; the last occurrence wins,
// because the environment follows the argv.
func psEnvValue(line, key string) string {
	re := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(key) + `=(\S*)`)
	matches := re.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// claudeJSONLockSuffix names agent-deck's own lock beside .claude.json. It
// must not be "<file>.lock": Claude Code may keep its own lock there.
const claudeJSONLockSuffix = ".agentdeck"

// updateClaudeJSONLocked applies mutate to the JSON object at path under an
// exclusive host-wide lock, writes it atomically, and re-reads it to prove
// the result landed. Parallel switches to one account (seven at once in the
// field) each read-modify-wrote the same file unlocked and lost each other's
// trust entries.
func updateClaudeJSONLocked(path string, mutate func(map[string]any)) error {
	lock, err := AcquireConfigFileLockTimeout(path+claudeJSONLockSuffix, 15*time.Second)
	if err != nil {
		return fmt.Errorf("lock %s: %w", path, err)
	}
	defer lock.Release()
	for attempt := 0; attempt < 2; attempt++ {
		cfg := map[string]any{}
		if data, readErr := os.ReadFile(path); readErr == nil {
			if len(bytes.TrimSpace(data)) > 0 {
				if err := json.Unmarshal(data, &cfg); err != nil {
					return fmt.Errorf("parse %s: %w", path, err)
				}
			}
		} else if !os.IsNotExist(readErr) {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		mutate(cfg)
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", path, err)
		}
		if err := atomicfile.WriteFile(path, out, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if claudeJSONAfterWrite != nil {
			claudeJSONAfterWrite(path)
		}
		// A Claude process of this account may rewrite the file from its
		// own memory at any moment; check that ours survived and redo once.
		check := map[string]any{}
		if data, readErr := os.ReadFile(path); readErr == nil && json.Unmarshal(data, &check) == nil {
			before, _ := json.Marshal(check)
			mutate(check)
			after, _ := json.Marshal(check)
			if bytes.Equal(before, after) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s was rewritten concurrently; pre-seed did not hold", path)
}

// claudeJSONAfterWrite runs right after each write; tests use it to stand in
// for a Claude process rewriting the file from its own memory.
var claudeJSONAfterWrite func(path string)

func jsonUnmarshalLoose(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// ClaudeAuthIdentity runs `claude auth status` for configDir, bounded in
// time and output, and returns the account e-mail (or organisation) it names.
func ClaudeAuthIdentity(configDir string) (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "auth", "status")
	// The child env is built through childenv: it must not inherit this
	// process's conductor CLAUDE_CONFIG_DIR or TELEGRAM_* (#1163).
	cmd.Env = ChildLaunchEnv(nil, ExpandPath(configDir))
	var buf cappedBuffer
	buf.max = 64 << 10
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	return parseClaudeAuthIdentity(buf.String(), runErr)
}

var claudeAuthEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

func parseClaudeAuthIdentity(out string, runErr error) (string, error) {
	var parsed struct {
		Email   string `json:"email"`
		Account struct {
			Email string `json:"email"`
		} `json:"account"`
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed) == nil {
		if parsed.LoggedIn != nil && !*parsed.LoggedIn {
			return "", fmt.Errorf("not logged in")
		}
		if parsed.Email != "" {
			return parsed.Email, nil
		}
		if parsed.Account.Email != "" {
			return parsed.Account.Email, nil
		}
	}
	if m := claudeAuthEmail.FindString(out); m != "" {
		return m, nil
	}
	if runErr != nil {
		return "", fmt.Errorf("claude auth status: %v", runErr)
	}
	return "", fmt.Errorf("claude auth status named no account")
}

type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}
