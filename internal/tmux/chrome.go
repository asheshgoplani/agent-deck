//go:build !windows

package tmux

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Terminal-chrome escape sequences. Agent-deck owns the outer terminal's
// tty between tea.Exec attach/detach boundaries, so it can write iTerm2-
// specific OSC sequences directly to os.Stdout — exactly the same pattern
// used by emitScrollbackClear for #618.
//
// This replaces the external poller in tarek-eq-scripts/hooks/iterm-badge-sync.sh,
// which had to walk pgrep → ppid → tty to find this same fd from outside
// the agent-deck process.
const (
	// itermBadgeOSCPrefix opens an OSC 1337 SetBadgeFormat sequence.
	// Format per iTerm2 docs: ESC ]1337;SetBadgeFormat=<base64> BEL.
	// An empty base64 payload clears the badge.
	itermBadgeOSCPrefix     = "\x1b]1337;SetBadgeFormat="
	itermBadgeOSCTerminator = "\a"
)

// iTerm2Active returns true when agent-deck is running inside an iTerm2
// terminal. Checks both TERM_PROGRAM (set on local launches) and
// LC_TERMINAL (auto-propagated over ssh when iTerm2's
// "Set locale variables automatically" option is on, which is the default).
// The bash hook used LC_TERMINAL because it's the most-propagating signal;
// keeping that fallback here preserves parity for SSH'd-in agent-deck.
func iTerm2Active() bool {
	if DetectTerminal() == "iterm2" {
		return true
	}
	return os.Getenv("LC_TERMINAL") == "iTerm2"
}

// iTermBadgeEffective resolves whether the iTerm2 badge feature should
// emit, given the config-sourced default. Mirrors the AGENTDECK_REPAINT
// precedent: a single env var named after the config key carries override
// semantics in its value (no awkward NO_ prefix).
//
// Tri-state precedence:
//   - AGENTDECK_ITERM_BADGE=1|true|yes|on   → force on, regardless of config.
//   - AGENTDECK_ITERM_BADGE=0|false|no|off  → force off, regardless of config.
//   - unset, empty, or unrecognised value   → defer to configEnabled.
//
// Garbage values intentionally fall through to config rather than failing
// closed; users who typo the env var still get their persistent setting.
func iTermBadgeEffective(configEnabled bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTDECK_ITERM_BADGE"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return configEnabled
}

// formatITermBadgeOSC returns the iTerm2 OSC 1337 SetBadgeFormat sequence
// for the given title (base64-encoded per iTerm2's wire format). An empty
// title produces the no-payload form, which iTerm2 interprets as "clear
// the badge".
//
// Pure formatter — no env / config / terminal-detection logic so it stays
// trivial to test. Both the direct-stdout (Attach) and via-tty (rename
// hook) emit paths share this byte sequence as their single source of truth.
func formatITermBadgeOSC(title string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(title))
	return itermBadgeOSCPrefix + encoded + itermBadgeOSCTerminator
}

// formatITermBadgeOSCViaTmux wraps formatITermBadgeOSC in a tmux DCS
// passthrough envelope so the OSC reaches the outer terminal even when
// emitted from inside a tmux pane (e.g. a Claude rename hook subprocess).
//
// Tmux DCS form: `ESC P tmux ; <inner with each ESC doubled> ESC \`. We
// take the inner OSC straight from formatITermBadgeOSC (single source of
// truth — the constants only live there) and ESC-double it. Works
// regardless of whether the pane has `allow-passthrough` on — the DCS
// envelope is the older, universally-supported mechanism.
func formatITermBadgeOSCViaTmux(title string) string {
	inner := strings.ReplaceAll(formatITermBadgeOSC(title), "\x1b", "\x1b\x1b")
	return "\x1bPtmux;" + inner + "\x1b\\"
}

// emitITermBadge writes the iTerm2 SetBadgeFormat OSC directly to w. Used
// on the Attach lifecycle boundary, where agent-deck owns the outer iTerm2
// tty (no tmux between us and the terminal), so a raw OSC suffices.
//
// Two gates, both must permit the emit:
//   - iTerm2Active() — terminal detection (TERM_PROGRAM or LC_TERMINAL).
//   - iTermBadgeEffective(configEnabled) — env-var-or-config decision.
func emitITermBadge(w io.Writer, title string, configEnabled bool) {
	if !iTerm2Active() || !iTermBadgeEffective(configEnabled) {
		return
	}
	_, _ = io.WriteString(w, formatITermBadgeOSC(title))
}

// EmitITermBadgeViaTty writes the SetBadgeFormat OSC to /dev/tty wrapped
// in a tmux DCS passthrough envelope. Used by Claude rename-hook
// subprocesses that run inside a tmux pane: their stdout is owned by the
// hook protocol (writing OSC there would corrupt the response), but their
// controlling tty is the tmux pane's pty, so a wrapped OSC routes through
// tmux out to iTerm2 — this is what closes the gap when Claude renames
// the session mid-attach.
//
// Silent no-op when:
//   - not iTerm2 (or env / config disabled)
//   - the process has no controlling tty (e.g. detached daemon)
//   - the user isn't attached to this pane in iTerm2 (tmux buffers the
//     pane output but iTerm2 never sees it — fine, the next attach emit
//     in pty.go will catch up)
//
// Debug: set AGENTDECK_ITERM_BADGE_DEBUG=1 to log decisions to
// /tmp/agent-deck-iterm-badge.log (timestamp, gate values, /dev/tty open
// result). Useful for diagnosing the multi-process chain Claude → hook
// subprocess → /dev/tty → tmux → iTerm2.
func EmitITermBadgeViaTty(title string, configEnabled bool) {
	dbg := emitITermBadgeDebugger()
	defer dbg.flush()

	dbg.logf("EmitITermBadgeViaTty title=%q configEnabled=%v", title, configEnabled)
	dbg.logf("  TERM_PROGRAM=%q LC_TERMINAL=%q ITERM_SESSION_ID=%q",
		os.Getenv("TERM_PROGRAM"), os.Getenv("LC_TERMINAL"), os.Getenv("ITERM_SESSION_ID"))
	dbg.logf("  AGENTDECK_ITERM_BADGE=%q", os.Getenv("AGENTDECK_ITERM_BADGE"))

	if !iTerm2Active() {
		dbg.logf("  decision: skip (iTerm2Active=false)")
		return
	}
	if !iTermBadgeEffective(configEnabled) {
		dbg.logf("  decision: skip (iTermBadgeEffective=false)")
		return
	}

	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		dbg.logf("  decision: skip (open /dev/tty failed: %v)", err)
		return
	}
	defer tty.Close()
	payload := formatITermBadgeOSCViaTmux(title)
	n, werr := io.WriteString(tty, payload)
	dbg.logf("  decision: wrote %d/%d bytes to /dev/tty err=%v", n, len(payload), werr)
}

// emitITermBadgeDebugger returns a debug helper that no-ops unless
// AGENTDECK_ITERM_BADGE_DEBUG=1 is set. Output goes to a per-user file in
// /tmp so the multi-process chain (agent-deck, tmux server, Claude, hook
// subprocess) all write to one log the user can `tail -f`.
func emitITermBadgeDebugger() *iTermBadgeDebugLog {
	if os.Getenv("AGENTDECK_ITERM_BADGE_DEBUG") != "1" {
		return &iTermBadgeDebugLog{}
	}
	path := fmt.Sprintf("/tmp/agent-deck-iterm-badge-%d.log", os.Getuid())
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return &iTermBadgeDebugLog{}
	}
	return &iTermBadgeDebugLog{f: f, pid: os.Getpid()}
}

type iTermBadgeDebugLog struct {
	f   *os.File
	pid int
}

func (d *iTermBadgeDebugLog) logf(format string, args ...any) {
	if d.f == nil {
		return
	}
	fmt.Fprintf(d.f, "%s pid=%d "+format+"\n",
		append([]any{time.Now().Format("15:04:05.000"), d.pid}, args...)...)
}

func (d *iTermBadgeDebugLog) flush() {
	if d.f != nil {
		_ = d.f.Close()
	}
}
