package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// seededSession is one row visualcheck put in the store before the binary
// under test ever starts, plus what it takes to find it again (tmux target
// for the live ones).
type seededSession struct {
	id, title, tool, group string
	tmuxName               string // set once started
}

type seed struct {
	claudeIdle, claudeWaiting, claudeRunning, claudeError, claudeStopped, claudeI18N seededSession
	shellLive                                                                        seededSession
	forkParentID                                                                     string
}

// seedStore drives the real CLI (add, group create, session start/stop, hook
// ingress via the fixture) to build a deterministic gallery: two groups with
// a subgroup, one session per tool, and every reachable status. Everything
// here is exercised through the binary under test itself — this is what "the
// sandbox store" being "seeded" means: production code paths write it, not a
// hand-crafted SQLite row (except where noted below).
func (s *suite) seedStore() (*seed, error) {
	for _, args := range [][]string{
		{"group", "create", "alpha"},
		{"group", "create", "backend", "--parent", "alpha"},
		{"group", "create", "beta"},
	} {
		if _, err := s.cmd(args...); err != nil {
			return nil, fmt.Errorf("seed %v: %w", args, err)
		}
	}

	// The first `group create` above just created state.db. Pre-answer the
	// one-time "install Claude Code hooks?" (and Hermes-hooks) consent
	// dialogs the same way a user's own click does -- by writing the same
	// metadata key production's confirm-dialog handler writes -- so the
	// list view is reachable on the very first frame instead of stuck
	// behind a modal. "declined" keeps our own hook-handler fixture calls
	// (fixtures.go) as the sole source of hook events, rather than having
	// the binary additionally rewrite ~/.claude/settings.json.
	if err := s.declineHookConsentDialogs(); err != nil {
		return nil, fmt.Errorf("seed hook consent: %w", err)
	}

	mk := func(name string) string {
		dir := filepath.Join(s.project, "work", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
		return dir
	}

	add := func(title, tool, group string) (seededSession, error) {
		out, err := s.cmd("add", "-c", tool, "-t", title, "-g", group, "--no-parent", "--json", mk(title))
		if err != nil {
			return seededSession{}, fmt.Errorf("add %s: %w", title, err)
		}
		var row struct {
			ID string `json:"id"`
		}
		if jsonErr := json.Unmarshal([]byte(out), &row); jsonErr != nil || row.ID == "" {
			return seededSession{}, fmt.Errorf("add %s: no id in %q", title, out)
		}
		return seededSession{id: row.ID, title: title, tool: tool, group: group}, nil
	}

	sd := &seed{}
	var err error

	// Idle gallery: one row per remaining tool, never started, so they sit
	// at the natural post-add StatusIdle. Covers "sessions of every tool".
	for _, t := range []struct{ title, tool, group string }{
		{"gemini-idle", "gemini", "beta"},
		{"opencode-idle", "opencode", "alpha"},
		{"codex-idle", "codex", "alpha/backend"},
		{"pi-idle", "pi", "beta"},
	} {
		if _, err = add(t.title, t.tool, t.group); err != nil {
			return nil, err
		}
	}

	if sd.claudeIdle, err = add("claude-idle", "claude", "alpha"); err != nil {
		return nil, err
	}

	if sd.claudeWaiting, err = add("claude-waiting", "claude", "alpha"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.claudeWaiting); err != nil {
		return nil, err
	}
	sd.forkParentID = sd.claudeWaiting.id

	if sd.claudeRunning, err = add("claude-running", "claude", "alpha/backend"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.claudeRunning); err != nil {
		return nil, err
	}
	if _, err = s.cmd("session", "send", "--no-wait", sd.claudeRunning.id, "VC_BUSY"); err != nil {
		return nil, fmt.Errorf("send VC_BUSY: %w", err)
	}
	if err = s.waitForPaneContains(sd.claudeRunning.tmuxName, "esc to interrupt", 15*time.Second); err != nil {
		return nil, fmt.Errorf("claude-running never showed busy banner: %w", err)
	}

	if sd.claudeError, err = add("claude-error", "claude", "alpha/backend"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.claudeError); err != nil {
		return nil, err
	}
	// classifyTerminatedPane (internal/session/instance.go) reads the last
	// recorded hook status when a vanished pane leaves no exit code: hook
	// "waiting" means a clean turn finished before the pane died →
	// StatusStopped, but hook "running" means a turn was still in flight →
	// StatusError. So mid-turn is where "error" has to be reached: send
	// VC_BUSY and wait for the busy banner (hookStatus becomes "running"),
	// then close stdin under it. This is the same crash path production
	// hits on a real interpreter dying mid-response, not a hand-set status
	// column.
	if _, err = s.cmd("session", "send", "--no-wait", sd.claudeError.id, "VC_BUSY"); err != nil {
		return nil, fmt.Errorf("send VC_BUSY to claude-error: %w", err)
	}
	if err = s.waitForPaneContains(sd.claudeError.tmuxName, "esc to interrupt", 15*time.Second); err != nil {
		return nil, fmt.Errorf("claude-error never showed busy banner: %w", err)
	}
	if _, err = s.exec("tmux", "send-keys", "-t", sd.claudeError.tmuxName, "C-d"); err != nil {
		return nil, fmt.Errorf("close claude-error stdin: %w", err)
	}
	if err = s.waitFor(15*time.Second, func() (bool, error) {
		row, showErr := s.show(sd.claudeError.id)
		if showErr != nil {
			return false, showErr
		}
		return row["status"] == "error", nil
	}); err != nil {
		return nil, fmt.Errorf("claude-error never reached StatusError: %w", err)
	}

	if sd.claudeStopped, err = add("claude-stopped", "claude", "alpha"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.claudeStopped); err != nil {
		return nil, err
	}
	if _, err = s.cmd("session", "stop", sd.claudeStopped.id); err != nil {
		return nil, fmt.Errorf("stop claude-stopped: %w", err)
	}

	if sd.claudeI18N, err = add("claude-i18n", "claude", "beta"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.claudeI18N); err != nil {
		return nil, err
	}
	if _, err = s.cmd("session", "send", "--no-wait", sd.claudeI18N.id, "VC_I18N"); err != nil {
		return nil, fmt.Errorf("send VC_I18N: %w", err)
	}
	if err = s.waitForPaneContains(sd.claudeI18N.tmuxName, "esc to interrupt", 15*time.Second); err != nil {
		return nil, fmt.Errorf("claude-i18n never went active: %w", err)
	}
	// Finalize directly over tmux, bypassing `session send`'s delivery
	// verification: VC_I18N's delivery is already proven by the busy banner
	// above, and the reply itself is instantaneous once finalized (see
	// fixtures.go), too fast for that verifier to see sustained "active".
	if _, err = s.exec("tmux", "send-keys", "-t", sd.claudeI18N.tmuxName, "VC_GO", "Enter"); err != nil {
		return nil, fmt.Errorf("finalize claude-i18n: %w", err)
	}
	if err = s.waitForPaneContains(sd.claudeI18N.tmuxName, visualCheckI18NReply, 15*time.Second); err != nil {
		return nil, fmt.Errorf("claude-i18n never showed its reply: %w", err)
	}
	// Let the backend's own status settle to "waiting" (the Stop hook fired
	// by fixtures.go's VC_GO branch) before any TUI ever looks at this row:
	// a session a fresh process's preview panel first observes mid
	// hook-status-transition was seen, during rehearsal, to get stuck
	// showing "Loading preview..." / a stale "starting" status indefinitely,
	// and to leave a torn partial repaint in the SESSIONS column once the
	// cursor then moved off it.
	if err = s.waitFor(15*time.Second, func() (bool, error) {
		row, showErr := s.show(sd.claudeI18N.id)
		if showErr != nil {
			return false, showErr
		}
		return row["status"] == "waiting", nil
	}); err != nil {
		return nil, fmt.Errorf("claude-i18n status never settled to waiting: %w", err)
	}

	if sd.shellLive, err = add("shell-live", "shell", "beta"); err != nil {
		return nil, err
	}
	if err = s.startAndAwaitHook(&sd.shellLive); err != nil {
		return nil, err
	}

	return sd, nil
}

// startAndAwaitHook starts a session and waits for its tmux pane to exist,
// filling in ss.tmuxName. For claude sessions the fixture fires its
// SessionStart/Stop hooks synchronously before printing its prompt, so by
// the time the pane exists the row is already past StatusStarting into
// StatusWaiting — the brief's "starting" state is too brief to seed reliably.
func (s *suite) startAndAwaitHook(ss *seededSession) error {
	if _, err := s.cmd("session", "start", ss.id); err != nil {
		return fmt.Errorf("start %s: %w", ss.title, err)
	}
	row, err := s.show(ss.id)
	if err != nil {
		return err
	}
	ss.tmuxName, _ = row["tmux_session"].(string)
	if ss.tmuxName == "" {
		return fmt.Errorf("start %s: no tmux_session in show output", ss.title)
	}
	return s.waitFor(10*time.Second, func() (bool, error) {
		_, err := s.capturePane(ss.tmuxName)
		return err == nil, nil
	})
}

// findStateDB locates the sandbox's per-profile state.db by walking the
// sandbox root: its exact path depends on internal/session's data-root
// resolution (XDG vs legacy vs profile name), which this tool doesn't need
// to replicate.
func (s *suite) findStateDB() (string, error) {
	var found string
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !info.IsDir() && filepath.Base(path) == "state.db" {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("state.db not found under %s after seeding", s.root)
	}
	return found, nil
}

func (s *suite) declineHookConsentDialogs() error {
	dbPath, err := s.findStateDB()
	if err != nil {
		return err
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, key := range []string{"hooks_prompted", "hermes_hooks_prompted"} {
		if err := db.SetMeta(key, "declined"); err != nil {
			return err
		}
	}
	return nil
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
