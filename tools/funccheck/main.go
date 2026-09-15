// funccheck drives a supplied agent-deck binary in a disposable environment.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type result struct {
	Name       string `json:"name"`
	Did        string `json:"did"`
	Observed   string `json:"observed"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
type report struct {
	BinarySHA256 string    `json:"binary_sha256"`
	Binary       string    `json:"binary"`
	Started      time.Time `json:"started"`
	Results      []result  `json:"checks"`
}
type suite struct {
	root, project, sourceDir       string
	bin, realTmux                  string
	sessionID, sessionTmux, socket string
	env                            []string
	results                        []result
	ctx                            context.Context
}

func main() { os.Exit(runMain(os.Args[1:])) }
func runMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: funccheck <agent-deck-binary>")
		return 2
	}
	if runtime.GOOS == "darwin" {
		fmt.Fprintln(os.Stderr, "Run make check-functional in Docker on macOS; native checks are reserved for CI/Linux.")
		return 2
	}
	bin, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s := &suite{bin: bin, ctx: ctx}
	// Register teardown before setup can spawn any process.
	defer s.cleanup()
	started := time.Now().UTC()
	s.run("sandbox", "Create a throwaway HOME and private tmux server", func() (string, error) {
		return "Private HOME, XDG directories and tmux socket; telemetry disabled", s.setup()
	})
	if !s.failed() {
		s.lifecycle()
		s.completion()
		s.auxiliary()
		s.tui()
		s.removePrimary()
	}
	s.run("sandbox teardown", "Stop the private tmux server and remove the throwaway HOME", func() (string, error) { return "Private server stopped and temporary files removed", s.teardown() })
	r := report{Binary: bin, BinarySHA256: binaryHash(bin), Started: started, Results: s.results}
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.WriteFile("funccheck-report.json", append(data, '\n'), 0600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		return 1
	}
	fmt.Println("\n| Capability | Result | What the user sees | Duration |\n|---|---|---|---|")
	for _, r := range s.results {
		detail := r.Observed
		if r.Reason != "" {
			detail = r.Reason
		}
		fmt.Printf("| %s | %s | %s | %d ms |\n", cell(r.Name), r.Status, cell(detail), r.DurationMS)
	}
	fmt.Println("\nJSON: funccheck-report.json. SKIPPED and UNKNOWN are unverified, never PASS.")
	if s.failed() {
		return 1
	}
	return 0
}
func cell(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(s) }
func (s *suite) run(name, did string, fn func() (string, error)) {
	start := time.Now()
	observed, err := fn()
	r := result{Name: name, Did: did, Observed: observed, Status: "PASS", DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		r.Status = "FAIL"
		r.Reason = err.Error()
	}
	s.results = append(s.results, r)
	fmt.Fprintf(os.Stderr, "%s: %s\n", r.Status, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, r.Reason)
	}
}
func (s *suite) skip(name, did, reason string) {
	s.results = append(s.results, result{Name: name, Did: did, Status: "SKIPPED", Reason: reason})
}
func (s *suite) unknown(name, did, reason string) {
	s.results = append(s.results, result{Name: name, Did: did, Status: "UNKNOWN", Reason: reason})
}
func (s *suite) failed() bool {
	for _, r := range s.results {
		if r.Status == "FAIL" {
			return true
		}
	}
	return false
}
func (s *suite) cmd(args ...string) (string, error) { return s.exec(s.bin, args...) }
func (s *suite) exec(name string, args ...string) (string, error) {
	return s.execIn(s.project, name, args...)
}
func (s *suite) execIn(dir, name string, args ...string) (string, error) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout(name))
	defer cancel()
	// LookPath uses the caller environment, so resolve shims explicitly.
	if !strings.ContainsRune(name, os.PathSeparator) {
		if p := filepath.Join(s.root, "bin", name); fileExecutable(p) {
			name = p
		}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{}, s.env...)
	if name == "go" {
		for _, k := range []string{"GOMODCACHE", "GOCACHE", "GOTOOLCHAIN", "GOFLAGS"} {
			if v := os.Getenv(k); v != "" {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
	}
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, text)
	}
	return text, nil
}
func fileExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0111 != 0
}
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (s *suite) setup() error {
	if !fileExecutable(s.bin) {
		return fmt.Errorf("target binary is not executable: %s", s.bin)
	}
	var err error
	s.sourceDir, err = os.Getwd()
	if err != nil {
		return err
	}
	s.realTmux, err = exec.LookPath("tmux")
	if err != nil {
		return err
	}
	s.root, err = os.MkdirTemp("/tmp", "fc-")
	if err != nil {
		return err
	}
	s.project = filepath.Join(s.root, "project")
	s.socket = fmt.Sprintf("funccheck-%d", os.Getpid())
	for _, p := range []string{"bin", "project", ".agent-deck", ".config", ".cache", ".local/state", ".local/share", "tmux"} {
		if err = os.MkdirAll(filepath.Join(s.root, p), 0700); err != nil {
			return err
		}
	}
	// Allowlist environment, following tests/eval/harness. Never inherit agent
	// identity, account/auth paths, TMUX, SSH agents or host XDG paths.
	s.env = []string{
		"HOME=" + s.root,
		"XDG_CONFIG_HOME=" + s.root + "/.config",
		"XDG_CACHE_HOME=" + s.root + "/.cache",
		"XDG_DATA_HOME=" + s.root + "/.local/share",
		"XDG_STATE_HOME=" + s.root + "/.local/state",
		"PATH=" + s.root + "/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"SHELL=/bin/bash",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=xterm-256color",
		"NO_COLOR=1",
		"AGENTDECK_COLOR=none",
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
		"AGENTDECK_TELEMETRY=0",
		"DO_NOT_TRACK=1",
		"TMUX_TMPDIR=" + s.root + "/tmux",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"CI=true",
	}
	// A wrapper is required: production invokes tmux by name. Unlike the eval
	// wrapper, refuse caller socket flags so no command can override isolation.
	wrapper := `#!/bin/sh
expect_value=0
for arg in "$@"; do
 if [ "$expect_value" = 1 ]; then expect_value=0; continue; fi
 case "$arg" in
  -S|-L|-S?*|-L?*) echo 'funccheck: refusing socket override' >&2; exit 64;;
  -f|-c|-T) expect_value=1;;
  -*) ;;
  *) break;;
 esac
done
exec ` + shQuote(s.realTmux) + " -L " + shQuote(s.socket) + " -f /dev/null \"$@\"\n"

	if err = os.WriteFile(filepath.Join(s.root, "bin", "tmux"), []byte(wrapper), 0700); err != nil {
		return err
	}
	if err = os.Symlink(s.bin, filepath.Join(s.root, "bin", "agent-deck")); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(s.root, ".agent-deck", "config.toml"), []byte("[telemetry]\ndisabled = true\n[tmux]\nlaunch_in_user_scope = false\n[updates]\nauto_update = false\n[worktree]\nbranch_prefix = \"\"\n"), 0600); err != nil {
		return err
	}
	if err = s.installClaude(); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Functional Check", "-c", "user.email=funccheck@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic fixture"}} {
		if _, err = s.exec("git", args...); err != nil {
			return err
		}
	}
	return nil
}
func (s *suite) cleanup() {
	if err := s.teardown(); err != nil {
		fmt.Fprintln(os.Stderr, "funccheck cleanup:", err)
	}
}
func (s *suite) teardown() error {
	if s.root == "" {
		return nil
	}
	if s.realTmux != "" && s.socket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// #nosec G204 -- real tmux path is resolved once; socket is our generated private name under the same isolated TMUX_TMPDIR.
		cmd := exec.CommandContext(ctx, s.realTmux, "-L", s.socket, "kill-server")
		// Construct socket resolution even if setup failed before s.env was built.
		cmd.Env = []string{"HOME=" + s.root, "TMUX_TMPDIR=" + filepath.Join(s.root, "tmux"), "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil && !tmuxAbsent(string(out)) {
			return fmt.Errorf("private tmux teardown: %w: %s; sandbox retained at %s", err, out, s.root)
		}
	}
	if err := os.RemoveAll(s.root); err != nil {
		return err
	}
	s.root = ""
	return nil
}
func tmuxAbsent(out string) bool {
	return strings.Contains(out, "no server running") || strings.Contains(out, "No such file or directory")
}
func (s *suite) show(id string) (map[string]any, error) {
	out, err := s.cmd("session", "show", "--json", id)
	if err != nil {
		return nil, err
	}
	var row map[string]any
	err = json.Unmarshal([]byte(out), &row)
	return row, err
}
func (s *suite) poll(fn func() error) error {
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = fn(); err == nil {
			return nil
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return err
}
func (s *suite) pane() (string, error) {
	out, err := s.exec("tmux", "list-panes", "-a", "-F", "#{session_name} #{pane_id} #{pane_dead}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, s.sessionTmux+" ") && s.sessionTmux != "" {
			f := strings.Fields(line)
			if len(f) == 3 && f[2] == "0" {
				return f[1], nil
			}
		}
	}
	return "", fmt.Errorf("live pane for %s absent: %s", s.sessionID, out)
}
func (s *suite) lifecycle() {
	s.run("session add", "Add a synthetic Claude session and read it back from list/show", func() (string, error) {
		if _, err := s.cmd("add", "-c", "claude", "-t", "functional-parent", s.project); err != nil {
			return "", err
		}
		row, err := s.show("functional-parent")
		if err != nil {
			return "", err
		}
		s.sessionID, _ = row["id"].(string)
		s.sessionTmux, _ = row["tmux_session"].(string)
		if s.sessionID == "" {
			return "", errors.New("show returned no session ID")
		}
		listed, err := s.sessionListed(s.sessionID)
		if err == nil && !listed {
			err = errors.New("added session absent from list")
		}
		return "Session appears in list with its assigned ID", err
	})
	if s.sessionID == "" {
		s.unknown("session lifecycle", "Start/send/output/stop/restart", "Add failed; no session available")
		return
	}
	s.run("session start", "Start the session and inspect the real tmux pane", func() (string, error) {
		if _, err := s.cmd("session", "start", s.sessionID); err != nil {
			return "", err
		}
		err := s.poll(func() error { _, err := s.pane(); return err })
		return "Live tmux pane exists on the private server", err
	})
	s.run("session send and output", "Send a unique prompt to the synthetic agent and capture its reply", func() (string, error) {
		if _, err := s.cmd("session", "send", "--no-wait", s.sessionID, "FUNCHECK_PARENT_PING"); err != nil {
			return "", err
		}
		err := s.poll(func() error {
			out, err := s.cmd("session", "output", s.sessionID)
			if err != nil {
				return err
			}
			if !strings.Contains(out, "fixture received: FUNCHECK_PARENT_PING") {
				return fmt.Errorf("fixture response absent from session output: %s", out)
			}
			return nil
		})
		return "Output contains the synthetic agent response to FUNCHECK_PARENT_PING", err
	})
	s.run("session stop", "Stop the session and verify both pane absence and stopped status", func() (string, error) {
		if _, err := s.cmd("session", "stop", s.sessionID); err != nil {
			return "", err
		}
		if err := s.paneAbsent(); err != nil {
			return "", err
		}
		row, err := s.show(s.sessionID)
		if err != nil {
			return "", err
		}
		if row["status"] != "stopped" {
			return "", fmt.Errorf("unexpected stopped status: %v", row["status"])
		}
		return fmt.Sprintf("Pane gone; list status is %v", row["status"]), nil
	})
	s.run("session restart", "Restart stopped session and confirm its ID and live pane", func() (string, error) {
		if _, err := s.cmd("session", "restart", s.sessionID); err != nil {
			return "", err
		}
		row, err := s.show(s.sessionID)
		if err != nil {
			return "", err
		}
		s.sessionTmux, _ = row["tmux_session"].(string)
		err = s.poll(func() error { _, err := s.pane(); return err })
		return "Same session has a live pane again", err
	})
}
func (s *suite) removePrimary() {
	if s.sessionID == "" {
		return
	}
	s.run("session remove", "Remove the session and verify list/show and pane absence", func() (string, error) {
		if _, err := s.cmd("session", "remove", "--force", s.sessionID); err != nil {
			return "", err
		}
		listed, err := s.sessionListed(s.sessionID)
		if err != nil {
			return "", err
		}
		if listed {
			return "", errors.New("removed session remains in list")
		}
		if err := s.requireMissing(s.sessionID); err != nil {
			return "", err
		}

		if err := s.paneAbsent(); err != nil {
			return "", err
		}
		return "Session is absent from list, show and tmux", nil
	})
}

func commandTimeout(name string) time.Duration {
	if filepath.Base(name) == "go" {
		return 4 * time.Minute
	}
	return 45 * time.Second
}

func binaryHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Absence is evidence only when tmux answers successfully or explicitly says
// the private server does not exist. Timeouts and execution failures fail.
func (s *suite) paneAbsent() error {
	return s.paneAbsentFor(s.sessionTmux)
}

func (s *suite) paneAbsentFor(tmuxName string) error {
	if tmuxName == "" {
		return errors.New("session tmux identity was never observed")
	}
	out, err := s.exec("tmux", "list-panes", "-a", "-F", "#{session_name}")
	if err != nil {
		if tmuxAbsent(out) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == tmuxName {
			return errors.New("session pane remains after removal")
		}
	}
	return nil
}
