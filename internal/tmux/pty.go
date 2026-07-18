//go:build !windows
// +build !windows

package tmux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/termreply"
	"github.com/creack/pty"
	"golang.org/x/term"
)

const attachOutputDrainTimeout = 250 * time.Millisecond

// attachReplyQuarantine is how long after attach/detach we filter
// terminal-generated control replies from stdin. Terminal capability
// reply bursts (DA1/DA2, OSC color queries, etc.) empirically complete
// within tens of milliseconds. 500ms gives comfortable margin while
// being short enough that the TUI does not feel frozen on return from
// an attached session.
const attachReplyQuarantine = 500 * time.Millisecond

// IndexDetachKey returns the index of a control-key sequence in data, or -1 if
// not found. detachByte is the raw ASCII byte (e.g. 0x11 for Ctrl+Q).
// Handles three encodings:
//   - Raw byte
//   - xterm modifyOtherKeys: ESC[27;5;{keyCode}~
//   - CSI u (kitty keyboard protocol): ESC[{keyCode};5u
func IndexDetachKey(data []byte, detachByte byte) int {
	if idx := bytes.IndexByte(data, detachByte); idx >= 0 {
		return idx
	}
	// Derive the printable key code for escape sequence matching.
	var keyCode byte
	if detachByte >= 1 && detachByte <= 26 {
		keyCode = detachByte + 96 // ctrl+letter: 1-26 -> 'a'-'z'
	} else if detachByte >= 28 && detachByte <= 31 {
		keyCode = detachByte + 64 // ctrl+special: 28-31 -> '\',']','^','_'
	}
	if keyCode > 0 {
		modSeq := fmt.Sprintf("\x1b[27;5;%d~", keyCode)
		if idx := bytes.Index(data, []byte(modSeq)); idx >= 0 {
			return idx
		}
		csiSeq := fmt.Sprintf("\x1b[%d;5u", keyCode)
		if idx := bytes.Index(data, []byte(csiSeq)); idx >= 0 {
			return idx
		}
	}
	return -1
}

// IndexCtrlQ returns the index of a Ctrl+Q sequence in data, or -1 if not found.
// This is a convenience wrapper around IndexDetachKey with the default Ctrl+Q byte.
func IndexCtrlQ(data []byte) int {
	return IndexDetachKey(data, 17)
}

// SwitchIntent reports why the attach loop handed control back to the caller:
// a plain detach/exit, an in-attach session switch, or an in-attach scrollback
// request (#1491).
type SwitchIntent int

const (
	// SwitchNone means no switch was requested (normal detach / process exit).
	SwitchNone SwitchIntent = iota
	// SwitchRequested means the user pressed the switch key while attached.
	SwitchRequested
	// ScrollbackRequested means the user pressed the scrollback key while
	// attached and the caller should open the in-view scrollback pager. The
	// deck's Enter-attach owns the viewport, so tmux's own copy-mode is
	// unreachable there (#1491); this intent is the escape hatch.
	ScrollbackRequested
)

// pageUpSeq is the exact CSI sequence a bare PageUp emits. Modified variants
// (Shift/Ctrl/Alt) carry a parameter — ESC[5;2~, ESC[5;5~, … — and do NOT match
// this literal, so they pass through to the attached program untouched. That is
// deliberate: the scrollback pager steals only the unmodified PageUp the issue
// reporter pressed expecting to scroll, leaving modified PageUp for pagers and
// editors running inside the session.
const pageUpSeq = "\x1b[5~"

// AttachOptions configures AttachWithOptions. The zero value attaches with the
// default Ctrl+Q detach key and no session-switch key.
type AttachOptions struct {
	// DetachByte is the raw control byte that detaches (0 => default Ctrl+Q).
	DetachByte byte
	// SwitchKeyByte is the control byte (e.g. Ctrl+S, 0x13) that hands control
	// back to the caller to open the in-attach session switcher. 0 disables it.
	//
	// This is deliberately a plain control byte, not Ctrl+Tab: terminals only
	// emit a distinct sequence for Ctrl+Tab under an enhanced keyboard protocol
	// that is not reliably available during attach, so a control byte is the
	// only portable trigger (the cycling/commit UX then lives in the TUI).
	SwitchKeyByte byte
	// ScrollbackKeyByte is a control byte (e.g. Ctrl+G, 0x07) that hands control
	// back to the caller to open the in-view scrollback pager (#1491). 0
	// disables the control-byte trigger; the PageUp trigger below is
	// independent.
	ScrollbackKeyByte byte
	// ScrollbackOnPageUp, when true, makes a bare PageUp (ESC[5~) open the
	// scrollback pager. Modified PageUp (Shift/Ctrl/Alt) always passes through.
	// This is the default trigger because it is exactly the key a user presses
	// expecting to scroll back through the session.
	ScrollbackOnPageUp bool
	// ScrollbackGate, when non-nil, is consulted the moment a bare PageUp is
	// seen (with ScrollbackOnPageUp set). Returning true opens the pager — the
	// behaviour when the gate is nil; returning false leaves the PageUp for the
	// attached program. It exists so the pager never hijacks PageUp from a
	// full-screen app (e.g. Claude fullscreen) that scrolls itself and keeps no
	// tmux scrollback for the pager to show. It is invoked ONLY when a PageUp is
	// actually present, so the per-press tmux query it typically performs is
	// cheap and never runs on ordinary keystrokes. It is NOT consulted for the
	// ScrollbackKeyByte chord, which is an explicit user opt-in.
	ScrollbackGate func() bool
}

// indexSwitchKey returns the index of the switch key in data and
// SwitchRequested, or (-1, SwitchNone) if it is absent or disabled. It handles
// the raw byte plus the xterm modifyOtherKeys and kitty CSI-u encodings (via
// IndexDetachKey). The caller resolves precedence against the detach key.
func indexSwitchKey(data []byte, opts AttachOptions) (int, SwitchIntent) {
	if opts.SwitchKeyByte == 0 {
		return -1, SwitchNone
	}
	if idx := IndexDetachKey(data, opts.SwitchKeyByte); idx >= 0 {
		return idx, SwitchRequested
	}
	return -1, SwitchNone
}

// indexScrollbackTrigger returns the index in data at which a scrollback
// request begins, or -1 if none is present or scrollback is disabled. It
// considers both configured triggers and returns the earliest match:
//   - the ScrollbackKeyByte control chord (raw byte + modifyOtherKeys + CSI-u
//     encodings, via IndexDetachKey), and
//   - a bare PageUp (ESC[5~) when ScrollbackOnPageUp is set.
//
// The caller resolves precedence against the detach and switch keys, both of
// which win a collision.
func indexScrollbackTrigger(data []byte, opts AttachOptions) int {
	best := -1
	consider := func(idx int) {
		if idx >= 0 && (best == -1 || idx < best) {
			best = idx
		}
	}
	if opts.ScrollbackKeyByte != 0 {
		consider(IndexDetachKey(data, opts.ScrollbackKeyByte))
	}
	if opts.ScrollbackOnPageUp {
		// Consult the gate only once a PageUp is actually present, so the tmux
		// alternate-screen query it performs never runs on ordinary keystrokes.
		// A closed gate suppresses the trigger, leaving PageUp for the app.
		if idx := bytes.Index(data, []byte(pageUpSeq)); idx >= 0 && scrollbackPageUpAllowed(opts) {
			consider(idx)
		}
	}
	return best
}

// scrollbackPageUpAllowed reports whether a bare PageUp should open the pager
// right now. With no gate configured it always does (legacy behaviour); a gate
// lets the caller pass PageUp through to the attached program — e.g. when the
// pane is in the alternate screen. See AttachOptions.ScrollbackGate.
func scrollbackPageUpAllowed(opts AttachOptions) bool {
	return opts.ScrollbackGate == nil || opts.ScrollbackGate()
}

// resolveAttachInterrupt finds the earliest interrupt key in a stdin chunk and
// reports its byte index plus the intent it maps to. Precedence on a tie is
// detach > switch > scrollback (distinct keys can't share an index, but the
// ordering guards against a misconfigured trigger shadowing a higher-priority
// one). It returns (-1, SwitchNone) when no interrupt key is present.
//
// The intent it returns is what the caller assigns to switchOutcome:
//   - SwitchNone         => detach (or nothing found),
//   - SwitchRequested    => open the session switcher,
//   - ScrollbackRequested => open the scrollback pager.
//
// Extracted from the stdin goroutine so the precedence is unit-testable without
// spawning a PTY.
func resolveAttachInterrupt(chunk []byte, detach byte, opts AttachOptions) (int, SwitchIntent) {
	detachIdx := IndexDetachKey(chunk, detach)
	switchIdx, switchIn := indexSwitchKey(chunk, opts)
	scrollIdx := indexScrollbackTrigger(chunk, opts)

	interruptIdx := -1
	outcome := SwitchNone
	if detachIdx >= 0 {
		interruptIdx = detachIdx
		outcome = SwitchNone
	}
	if switchIdx >= 0 && (interruptIdx == -1 || switchIdx < interruptIdx) {
		interruptIdx = switchIdx
		outcome = switchIn
	}
	if scrollIdx >= 0 && (interruptIdx == -1 || scrollIdx < interruptIdx) {
		interruptIdx = scrollIdx
		outcome = ScrollbackRequested
	}
	return interruptIdx, outcome
}

func waitForAttachOutputDrain(outputDone <-chan struct{}, timeout time.Duration) (bool, time.Duration) {
	start := time.Now()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-outputDone:
		return true, time.Since(start)
	case <-timer.C:
		return false, time.Since(start)
	}
}

// Scrollback-clear escape sequences. See emitScrollbackClear below for the
// full rationale on why iTerm2 3.6.x requires the OSC 1337 supplement (#618).
const (
	// clearScrollbackCSI is CSI 3 J — "Erase Saved Lines". Honored by Terminal.app,
	// WezTerm, Alacritty, Ghostty, Kitty, xterm, and older iTerm2 builds.
	clearScrollbackCSI = "\x1b[3J"
	// itermClearScrollback is OSC 1337 ; ClearScrollback BEL — iTerm2-specific.
	// Required by iTerm2 3.6.x when "Save lines to scrollback in alternate screen
	// mode" is OFF (#618). Other terminals parse the OSC payload and discard it
	// safely — adding this escape is strictly additive, no regression risk.
	itermClearScrollback = "\x1b]1337;ClearScrollback\a"
)

// emitScrollbackClear writes escape sequences to clear the host terminal's
// scrollback buffer. Both the generic CSI 3 J escape AND the iTerm2-specific
// OSC 1337 ClearScrollback escape are emitted, in that order:
//
//   - CSI first — broadly-compatible, terminals that honor it short-circuit.
//   - OSC second — belt-and-suspenders for iTerm2 3.6.x where CSI alone is
//     insufficient when alt-screen-scrollback-save is disabled (#618 regression
//     of #419).
//
// Both Attach() entry and cleanupAttach() (exit) route through this helper so
// the two boundaries cannot silently drift apart (parallel-paths invariant).
func emitScrollbackClear(w io.Writer) {
	_, _ = io.WriteString(w, clearScrollbackCSI)
	_, _ = io.WriteString(w, itermClearScrollback)
}

// StartAttachPTY starts cmd attached to a new PTY pre-sized to tty's current
// dimensions.
//
// #1167: tmux clients connect at their PTY's size. A detached `new-session`
// (no -x/-y) is born at tmux's 80x24 default-size, and a bare pty.Start creates
// the attach client's PTY at the same 80x24 default — so window-size=largest
// pins the window to 80 cols, ~half of a wide terminal, until an async SIGWINCH
// grows it. Reading the controlling terminal's real size up front and starting
// the PTY with it makes the client full-width from frame one.
//
// When tty is not a terminal (size probe fails), it falls back to a plain start
// at the default size: a degraded attach is still better than no attach.
func StartAttachPTY(cmd *exec.Cmd, tty *os.File) (*os.File, error) {
	if tty != nil {
		if ws, err := pty.GetsizeFull(tty); err == nil && ws.Cols > 0 && ws.Rows > 0 {
			return pty.StartWithSize(cmd, ws)
		}
	}
	return pty.Start(cmd)
}

// Attach attaches to the tmux session with full PTY support.
// The configured detach key (default Ctrl+Q) will detach and return to the caller.
// Pass an optional detachByte to override the default (0x11 / Ctrl+Q).
//
// Attach is a thin wrapper over AttachWithOptions that ignores the returned
// SwitchIntent — use it when session-switch keys are not needed.
func (s *Session) Attach(ctx context.Context, detachByte ...byte) error {
	var detach byte
	if len(detachByte) > 0 {
		detach = detachByte[0]
	}
	_, err := s.AttachWithOptions(ctx, AttachOptions{DetachByte: detach})
	return err
}

// AttachWithOptions attaches to the tmux session with full PTY support and the
// session-switch keys configured in opts. It returns the SwitchIntent the user
// requested (SwitchNone on a normal detach or when the pane process exits) so
// the caller can open an in-attach session switcher.
func (s *Session) AttachWithOptions(ctx context.Context, opts AttachOptions) (SwitchIntent, error) {
	detach := byte(17) // Ctrl+Q default
	if opts.DetachByte != 0 {
		detach = opts.DetachByte
	}

	if !s.Exists() {
		return SwitchNone, fmt.Errorf("session %s does not exist", s.Name)
	}

	// Clear the outer terminal emulator's scrollback buffer to prevent
	// stale content from a previously-attached session bleeding into the
	// new one (#419, #618). emitScrollbackClear writes both the generic
	// CSI 3 J escape AND the iTerm2-specific OSC 1337 ClearScrollback escape —
	// the latter is required for iTerm2 3.6.x where CSI alone is insufficient
	// when alt-screen-scrollback-save is disabled (#618 regression of #419).
	//
	// Note: We intentionally do NOT call `tmux clear-history` here. tmux pane
	// histories are per-pane, so session A's output never appears in session B's
	// scrollback. Clearing pane history on attach destroys the user's scrollback
	// and breaks mouse-wheel / copy-mode navigation (#531).
	emitScrollbackClear(os.Stdout)

	// Set the iTerm2 badge to the session's display title for the duration
	// of the attach. Agent-deck owns the outer iTerm2 tty here (no tmux
	// between us and the terminal), so a direct OSC write reaches iTerm2.
	// Replaces the external pgrep/ppid/tty-walk in iterm-badge-sync.sh.
	// Opt-in: no-op outside iTerm2, when [terminal].iterm_badge=false in
	// user config (the default), or when AGENTDECK_ITERM_BADGE=0 forces
	// it off at runtime. AGENTDECK_ITERM_BADGE=1 ad-hoc enables.
	emitITermBadge(os.Stdout, s.DisplayName, s.terminalChromeIsEnabled())

	// Create context with cancel for detach
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// #1114: subscribe to mid-attach badge updates from the Claude
	// rename hook. The hook subprocess has no controlling tty (Claude
	// spawns hooks detached via setsid), so its EmitITermBadgeViaTty
	// path silently no-ops. Instead, the hook drops a file under
	// ~/.agent-deck/badge-updates/ and this goroutine — which DOES own
	// the outer iTerm2 tty via os.Stdout — re-emits the OSC. Stopped
	// by the deferred cancel above when Attach returns (detach).
	//
	// MUST be launched AFTER the context.WithCancel call: the TUI's
	// attachCmd.Run passes context.Background(), so a goroutine started
	// with the pre-WithCancel ctx is never stopped. That ordering bug
	// leaked one goroutine (250ms poll ticker) plus one fsnotify
	// watcher (inotify fd + epoll fd on Linux) per attach — hundreds
	// of watchers on the badge-updates dir inode and double-digit
	// sustained CPU after a day of deck hopping.
	go WatchBadgeUpdates(ctx, s.Name, os.Stdout, s.terminalChromeIsEnabled(), nil)

	// Start tmux attach command with PTY.
	// Routes through s.attachCmd → s.tmuxCmdContext so the -L <SocketName>
	// selector lands before the subcommand. Pre-v1.7.55 built argv by hand
	// and silently attached to the user's default server (#687 follow-up).
	cmd := s.attachCmd(ctx)

	// Temporarily ignore SIGINT for the duration of the attach session.
	// The global SIGINT handler in main.go calls os.Exit(0); suppressing
	// delivery during attach prevents the race window between tea.Exec
	// restoring the terminal and Attach() calling term.MakeRaw().
	// SIGINT is restored in cleanupAttach() via signal.Reset(syscall.SIGINT).
	signal.Ignore(syscall.SIGINT)
	// Safety net: restore SIGINT on every return path. cleanupAttach() resets it
	// first thing on the normal teardown, but the raw-mode setup failures below
	// return before cleanupAttach runs — without this defer they would leave the
	// process permanently ignoring Ctrl+C. signal.Reset is idempotent, so the
	// extra call on the happy path is harmless.
	defer signal.Reset(syscall.SIGINT)

	// Start command with PTY, pre-sized to the controlling terminal so the
	// tmux client connects full-width from frame one (#1167).
	ptmx, err := StartAttachPTY(cmd, os.Stdin)
	if err != nil {
		return SwitchNone, fmt.Errorf("failed to start pty: %w", err)
	}
	defer ptmx.Close()

	// Set the PTY to raw mode so all bytes pass through transparently.
	// Without this, the PTY's default terminal settings (ISIG enabled)
	// interpret Ctrl+Z as SUSP and send SIGTSTP to the tmux attach process,
	// causing it to exit and returning the user to the session list.
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		return SwitchNone, fmt.Errorf("failed to set pty raw mode: %w", err)
	}

	// Save original terminal state and set raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return SwitchNone, fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Handle window resize signals
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	sigwinchDone := make(chan struct{}) // Signal for SIGWINCH goroutine to exit
	defer func() {
		signal.Stop(sigwinch)
		close(sigwinchDone) // Signal goroutine to exit
		// Don't close sigwinch - signal.Stop() handles cleanup
	}()

	// WaitGroup to track ALL goroutines (including SIGWINCH handler)
	var wg sync.WaitGroup

	// SIGWINCH handler goroutine - properly tracked in WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sigwinchDone:
				return
			case _, ok := <-sigwinch:
				if !ok {
					return
				}
				if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, ws)
				}
			}
		}
	}()
	// Initial resize
	sigwinch <- syscall.SIGWINCH

	// Channel to signal detach
	detachCh := make(chan struct{})

	// switchOutcome is written by the stdin goroutine before it closes
	// detachCh when a session-switch key is pressed. The close establishes a
	// happens-before edge, so the main goroutine can read it after <-detachCh
	// without additional synchronization. It stays SwitchNone for a plain
	// detach or a pane-process exit.
	var switchOutcome SwitchIntent

	// Channel for I/O errors (buffered to prevent goroutine leaks)
	ioErrors := make(chan error, 2)

	startTime := time.Now()
	const terminalStyleReset = "\x1b]8;;\x1b\\\x1b[0m\x1b[24m\x1b[39m\x1b[49m"
	outputDone := make(chan struct{})

	// Goroutine 1: Copy PTY output to stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(outputDone)
		_, err := io.Copy(os.Stdout, ptmx)
		if err != nil && err != io.EOF {
			// Only report non-EOF errors (EOF is normal on PTY close)
			select {
			case ioErrors <- fmt.Errorf("PTY read error: %w", err):
			default:
				// Channel full, error already reported
			}
		}
	}()

	// Goroutine 2: Read stdin, intercept detach key, forward rest to PTY
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32)
		var replyFilter termreply.Filter
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err == io.EOF {
					break
				}
				// Report stdin read error
				select {
				case ioErrors <- fmt.Errorf("stdin read error: %w", err):
				default:
				}
				return
			}

			chunk := buf[:n]
			// Always run the reply filter: escape-string replies (DCS/OSC/etc.)
			// can arrive long after the initial quarantine (e.g. iTerm2
			// XTVERSION reply on window focus/resize — #731). `armed` stays
			// gated to the quarantine window so generic CSI pass-through
			// works for keyboard input outside it.
			armed := time.Since(startTime) < attachReplyQuarantine
			chunk = replyFilter.Consume(chunk, armed, false)
			if len(chunk) == 0 {
				continue
			}

			// Check for the detach key and any session-switch keys anywhere in
			// the input chunk. Some terminals coalesce reads, so these must not
			// require a single-byte read. Handles raw byte, xterm
			// modifyOtherKeys, and kitty CSI u encodings.
			// Whichever interrupt key appears first in the buffer wins, with
			// detach > switch > scrollback precedence on a tie. Resolved by a
			// pure helper so the precedence is unit-testable.
			interruptIdx, outcome := resolveAttachInterrupt(chunk, detach, opts)

			if interruptIdx >= 0 {
				// Forward any bytes before the interrupt key, then stop.
				if interruptIdx > 0 {
					if _, err := ptmx.Write(chunk[:interruptIdx]); err != nil {
						select {
						case ioErrors <- fmt.Errorf("PTY write error: %w", err):
						default:
						}
						return
					}
				}
				switchOutcome = outcome
				close(detachCh)
				cancel()
				return
			}

			// Forward other input to tmux PTY
			if _, err := ptmx.Write(chunk); err != nil {
				// Report PTY write error
				select {
				case ioErrors <- fmt.Errorf("PTY write error: %w", err):
				default:
				}
				return
			}
		}
	}()

	// Wait for command to finish - tracked in WaitGroup
	cmdDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmdDone <- cmd.Wait()
	}()

	didDetach := false

	// Ensures we don't return to Bubble Tea while PTY output is still being written.
	// This avoids terminal style leakage (for example underline/hyperlink state)
	// from the attached client into the Agent Deck UI.
	cleanupAttach := func() {
		// Restore SIGINT handling before returning to TUI.
		// This must be the first operation so that SIGINT can terminate the
		// process if needed after the attach session ends.
		signal.Reset(syscall.SIGINT)
		cancel()
		_ = ptmx.Close()
		_, _ = waitForAttachOutputDrain(outputDone, attachOutputDrainTimeout)
		// Prompts can issue terminal capability/color queries as they redraw during
		// detach. Kitty replies on stdin; if those queued bytes survive until Bubble Tea
		// resumes, they can leak as literal fragments like terminal version strings or
		// rgb payloads in the TUI.
		if didDetach {
			_ = flushDetachInput(int(os.Stdin.Fd()))
			termreply.QuarantineFor(attachReplyQuarantine)
		}
		// Clear host terminal scrollback before returning to TUI.
		// The on-attach clear at the top of Attach() covers the "next attach" direction;
		// this covers the "on detach" direction for belt-and-suspenders coverage
		// (#419, #618). emitScrollbackClear emits CSI 3 J + iTerm2-specific OSC 1337
		// ClearScrollback — both boundaries route through one helper so they cannot drift.
		emitScrollbackClear(os.Stdout)
		// Clear the iTerm2 badge so the home view doesn't keep showing the
		// detached session's title. Symmetric with the on-entry emit above —
		// both boundaries route through emitITermBadge so they cannot drift.
		emitITermBadge(os.Stdout, "", s.terminalChromeIsEnabled())
		// Reset OSC-8 hyperlink state + SGR attributes before Bubble Tea redraws.
		_, _ = os.Stdout.WriteString(terminalStyleReset)
	}

	// Wait for either detach or command completion
	var attachErr error
	select {
	case <-detachCh:
		// User pressed the detach key, detach gracefully
		didDetach = true
		attachErr = nil
	case err := <-cmdDone:
		if err != nil {
			// Check if it's a normal exit (tmux detach via Ctrl+B,D)
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
					attachErr = nil
				} else {
					attachErr = err
				}
			} else {
				attachErr = err
			}
			// Context cancelled is normal (from detach key)
			if ctx.Err() != nil {
				attachErr = nil
			}
		} else {
			attachErr = nil
		}
	case <-ctx.Done():
		attachErr = nil
	}

	cleanupAttach()
	return switchOutcome, attachErr
}

// AttachWindow attaches to a specific window within this tmux session.
// Selects the target window first, then uses the standard Attach flow.
func (s *Session) AttachWindow(ctx context.Context, windowIndex int, detachByte ...byte) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Select the target window before attaching. Routes through
	// s.selectWindowCmd → s.tmuxCmd so isolation-configured sessions
	// don't select a same-named window on the default server (#687).
	if err := s.selectWindowCmd(windowIndex).Run(); err != nil {
		target := fmt.Sprintf("%s:%d", s.Name, windowIndex)
		return fmt.Errorf("failed to select window %s: %w", target, err)
	}

	return s.Attach(ctx, detachByte...)
}

// Resize changes the terminal size of the tmux session
func (s *Session) Resize(cols, rows int) error {
	// Resize the tmux window. Routes through s.resizeCmd so isolation-
	// configured sessions resize the real pane, not a default-server ghost
	// (#687 follow-up).
	if err := s.resizeCmd(cols, rows).Run(); err != nil {
		return fmt.Errorf("failed to resize window: %w", err)
	}
	return nil
}

// AttachReadOnly attaches to the session in read-only mode
func (s *Session) AttachReadOnly(ctx context.Context) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Save original terminal state
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// Start tmux attach command in read-only mode. Routes through
	// s.attachReadOnlyCmd so read-only attach respects socket isolation
	// (#687 follow-up).
	cmd := s.attachReadOnlyCmd(ctx)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start the attach command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}

	// Wait for command to finish
	if err := cmd.Wait(); err != nil {
		// Check if it's a normal detach
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
				return nil
			}
		}
		return fmt.Errorf("attach command failed: %w", err)
	}

	return nil
}

// StreamOutput streams the session output to the provided writer
func (s *Session) StreamOutput(ctx context.Context, w io.Writer) error {
	if !s.Exists() {
		return fmt.Errorf("session %s does not exist", s.Name)
	}

	// Use tmux pipe-pane to stream output. Routes through
	// s.pipePaneStartCmd so the stream targets the session's actual server
	// under socket isolation (#687 follow-up).
	cmd := s.pipePaneStartCmd(ctx)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pipe-pane: %w", err)
	}

	// Wait for context cancellation or command completion
	// Use WaitGroup to prevent goroutine leak on context cancellation
	var wg sync.WaitGroup
	errChan := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errChan <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Stop pipe-pane - error is intentionally ignored since we're
		// already returning ctx.Err() and cleanup failure is non-fatal.
		// Socket-aware via s.pipePaneStopCmd (#687 follow-up).
		_ = s.pipePaneStopCmd().Run()
		// Wait for the goroutine to complete before returning
		wg.Wait()
		return ctx.Err()
	case err := <-errChan:
		if err != nil {
			return fmt.Errorf("pipe-pane failed: %w", err)
		}
		return nil
	}
}

// The following Session command-builder helpers are the seams the
// socket-isolation-at-attach fix (#687 follow-up, v1.7.55) routes
// through. Each returns an *exec.Cmd via s.tmuxCmd / s.tmuxCmdContext so
// every tmux subprocess spawned for this session carries `-L <SocketName>`
// when isolation is configured, and byte-identical plain argv when it is
// not. Keeping these as named methods gives the regression lint a stable
// target to assert argv shape against without spawning PTYs.

func (s *Session) attachCmd(ctx context.Context) *exec.Cmd {
	return s.tmuxCmdContext(ctx, "attach-session", "-t", s.Name)
}

func (s *Session) attachReadOnlyCmd(ctx context.Context) *exec.Cmd {
	return s.tmuxCmdContext(ctx, "attach-session", "-r", "-t", s.Name)
}

func (s *Session) resizeCmd(cols, rows int) *exec.Cmd {
	return s.tmuxCmd(
		"resize-window", "-t", s.Name,
		"-x", fmt.Sprintf("%d", cols),
		"-y", fmt.Sprintf("%d", rows),
	)
}

func (s *Session) selectWindowCmd(windowIndex int) *exec.Cmd {
	target := fmt.Sprintf("%s:%d", s.Name, windowIndex)
	return s.tmuxCmd("select-window", "-t", target)
}

func (s *Session) pipePaneStartCmd(ctx context.Context) *exec.Cmd {
	return s.tmuxCmdContext(ctx, "pipe-pane", "-t", s.Name, "-o", "cat")
}

func (s *Session) pipePaneStopCmd() *exec.Cmd {
	return s.tmuxCmd("pipe-pane", "-t", s.Name)
}
