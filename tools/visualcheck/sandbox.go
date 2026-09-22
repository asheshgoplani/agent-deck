// visualcheck drives a real agent-deck binary through every TUI screen in a
// private tmux server with a sandboxed HOME, capturing text frames at fixed
// widths and diffing them against committed goldens. This file builds the
// sandbox: throwaway HOME/XDG dirs, a tmux wrapper that pins our private
// socket, and fixture tool scripts (claude/gemini/opencode/codex/pi/shell)
// standing in for AI CLIs. Adapted from tools/funccheck's sandbox, which
// proved this isolation pattern; duplicated rather than imported because
// funccheck is its own package main.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// fixedLengthHex returns n random lowercase hex characters (n/2 random
// bytes). See its call site in setup for why the length must be fixed.
func fixedLengthHex(n int) (string, error) {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf)[:n], nil
}

type suite struct {
	root, project, sourceDir string
	bin, realTmux            string
	socket                   string
	env                      []string
	ctx                      context.Context
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
	// A fixed-length suffix, not os.MkdirTemp's variable-digit-count one and
	// not the PID: this path (and the tmux socket name, same reasoning)
	// gets embedded verbatim in rendered dialogs (e.g. the MCP manager's
	// "edit: <path>/config.toml" line), and several of those dialogs size
	// their bounding box to their longest line's rendered width. A
	// different suffix length between runs shifts that box's border
	// column for the WHOLE dialog, not just the one line with the path in
	// it -- a difference no post-hoc text scrub can undo. A constant
	// length keeps every run's rendered width identical regardless of the
	// random bytes' value.
	suffix, err := fixedLengthHex(10)
	if err != nil {
		return err
	}
	s.root = filepath.Join("/tmp", "vc-"+suffix)
	if err = os.Mkdir(s.root, 0700); err != nil {
		return err
	}
	s.project = filepath.Join(s.root, "project")
	socketSuffix, err := fixedLengthHex(8)
	if err != nil {
		return err
	}
	s.socket = "vc-" + socketSuffix
	for _, p := range []string{"bin", "project", ".agent-deck", ".config", ".cache", ".local/state", ".local/share", "tmux"} {
		if err = os.MkdirAll(filepath.Join(s.root, p), 0700); err != nil {
			return err
		}
	}
	// Allowlist environment, following tools/funccheck and tests/eval/harness.
	// Never inherit agent identity, account/auth paths, TMUX, SSH agents or
	// host XDG paths.
	s.env = []string{
		"HOME=" + s.root,
		"XDG_CONFIG_HOME=" + s.root + "/.config",
		"XDG_CACHE_HOME=" + s.root + "/.cache",
		"XDG_DATA_HOME=" + s.root + "/.local/share",
		"XDG_STATE_HOME=" + s.root + "/.local/state",
		"PATH=" + s.root + "/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"SHELL=/bin/sh",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=xterm-256color",
		"NO_COLOR=",
		"AGENTDECK_COLOR=full",
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
		"AGENT_DECK_ALLOW_NO_TTY=1",
		"AGENT_DECK_ALLOW_OUTER_TMUX=1",
		"AGENTDECK_TELEMETRY=0",
		"DO_NOT_TRACK=1",
		"TMUX_TMPDIR=" + s.root + "/tmux",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"CI=true",
	}
	// A wrapper is required: production invokes tmux by name. Refuse caller
	// socket flags so no command started under this sandbox can escape the
	// private server (same technique as funccheck).
	wrapper := `#!/bin/sh
expect_value=0
for arg in "$@"; do
 if [ "$expect_value" = 1 ]; then expect_value=0; continue; fi
 case "$arg" in
  -S|-L|-S?*|-L?*) echo 'visualcheck: refusing socket override' >&2; exit 64;;
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
	cfg := "[telemetry]\ndisabled = true\n[tmux]\nlaunch_in_user_scope = false\n" +
		"[updates]\nauto_update = false\nstartup_check = false\n[worktree]\nbranch_prefix = \"\"\n" +
		// The group-view step (steps.go) launches a second, short-lived
		// `agent-deck --group alpha` alongside the main width-run window to
		// capture the scoped view (SetGroupScope is launch-flag-only, not
		// reachable by any keypress); that needs the single-instance gate
		// (issue #1246) opened for this sandbox only.
		"[instances]\nallow_multiple = true\n" +
		"\n[remotes.lab]\nhost = \"fixture@vc-lab.invalid\"\nagent_deck_path = \"agent-deck\"\n"
	if err = os.WriteFile(filepath.Join(s.root, ".agent-deck", "config.toml"), []byte(cfg), 0600); err != nil {
		return err
	}
	if err = s.installFixtureTools(); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Visual Check", "-c", "user.email=visualcheck@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic fixture"}} {
		if _, err = s.exec("git", args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *suite) cleanup() {
	if os.Getenv("VISUALCHECK_KEEP_SANDBOX") == "1" {
		fmt.Fprintln(os.Stderr, "VISUALCHECK_KEEP_SANDBOX=1: leaving sandbox at", s.root, "(tmux socket", s.socket+")")
		return
	}
	if err := s.teardown(); err != nil {
		fmt.Fprintln(os.Stderr, "visualcheck cleanup:", err)
	}
}

func (s *suite) teardown() error {
	if s.root == "" {
		return nil
	}
	if s.realTmux != "" && s.socket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// #nosec G204 -- real tmux path resolved once; socket is our generated private name.
		cmd := exec.CommandContext(ctx, s.realTmux, "-L", s.socket, "kill-server")
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
	if !strings.ContainsRune(name, os.PathSeparator) {
		if p := filepath.Join(s.root, "bin", name); fileExecutable(p) {
			name = p
		}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{}, s.env...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, text)
	}
	return text, nil
}

func commandTimeout(name string) time.Duration {
	if filepath.Base(name) == "go" {
		return 4 * time.Minute
	}
	return 45 * time.Second
}
