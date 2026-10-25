package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// keySenderExec is a swappable seam for the tmux subprocesses spawned by the
// SendKeys / SendEnter / SendNamedKey key-delivery primitives. It defaults to
// tmuxExec (the real `tmux` binary) in production; tests override it to record
// the exact key sequence emitted without standing up a real tmux server. See
// the vim-mode regression test in tmux_vim_mode_test.go (issue #1264).
var keySenderExec = tmuxExec

// SendKeys sends keys to the tmux session
// Uses -l flag to treat keys as literal text, preventing tmux special key interpretation
func (s *Session) SendKeys(keys string) error {
	return s.sendKeysToTarget(s.Name, keys)
}

// windowTarget returns the tmux target addressing a specific window index
// within this session (e.g. "agentdeck_foo_ab12:2"), mirroring the format used
// by CaptureWindowFullHistory.
func (s *Session) windowTarget(windowIndex int) string {
	return fmt.Sprintf("%s:%d", s.TmuxName(), windowIndex)
}

// primaryWindowTarget addresses the first window in the session. tmux's ^
// selector is independent of base-index and remains stable when an operator
// opens or focuses auxiliary windows alongside the managed agent.
func (s *Session) primaryWindowTarget() string {
	return s.TmuxName() + ":^"
}

// SendKeysToPrimaryWindow sends literal text to the managed agent window.
func (s *Session) SendKeysToPrimaryWindow(keys string) error {
	return s.sendKeysToTarget(s.primaryWindowTarget(), keys)
}

// sendKeysToTarget sends literal text to an explicit tmux target — either the
// session name (active window) or a "<session>:<windowIndex>" window target.
// SendKeys delegates here against the active window.
func (s *Session) sendKeysToTarget(target, keys string) error {
	s.invalidateCache()
	// The -l flag makes tmux treat the string as literal text, not key names
	// This prevents issues like "Enter" being interpreted as the Enter key
	// and provides a layer of safety against tmux special sequences
	cmd := keySenderExec(s.SocketName, "send-keys", "-l", "-t", target, "--", escapeTrailingSemicolon(keys))
	return runSendKeysBounded(cmd)
}

// escapeTrailingSemicolon protects a trailing ';' from tmux: an argument
// that ends in ';' is a command separator even after -l, so "/foo a;" would
// type "/foo a". tmux reads "\;" at the end of an argument as a literal ';'.
func escapeTrailingSemicolon(keys string) string {
	if strings.HasSuffix(keys, ";") {
		return keys[:len(keys)-1] + `\;`
	}
	return keys
}

// ensureInsertMode prepends an Escape + `i` sequence so a vim-mode composer
// (Claude Code with "editorMode": "vim") is guaranteed to be in insert mode
// before text or Enter is delivered. No-op unless VimMode is set. The sequence
// is idempotent — Escape lands in normal mode, `i` enters insert — so it is
// safe to call when the prompt is already in insert mode. See issue #1264.
func (s *Session) ensureInsertMode() {
	s.ensureInsertModeOnTarget(s.Name)
}

// ensureInsertModeOnTarget is ensureInsertMode against an explicit tmux target.
func (s *Session) ensureInsertModeOnTarget(target string) {
	_ = s.ensureInsertModeOnTargetChecked(target, nil)
}

func (s *Session) ensureInsertModeOnTargetChecked(target string, before func() error) error {
	if !s.VimMode {
		return nil
	}
	// Escape: guarantee normal mode regardless of current state.
	if err := beforeKeyBatch(before); err != nil {
		return err
	}
	_ = runSendKeysBounded(keySenderExec(s.SocketName, "send-keys", "-t", target, "Escape"))
	// i: enter insert mode so the following paste/Enter are taken literally.
	if err := beforeKeyBatch(before); err != nil {
		return err
	}
	_ = runSendKeysBounded(keySenderExec(s.SocketName, "send-keys", "-t", target, "i"))
	return nil
}

func beforeKeyBatch(before func() error) error {
	if before != nil {
		return before()
	}
	return nil
}

// sendEnterRaw emits a single Enter keystroke without the vim-mode insert
// guard. Used internally by SendKeysAndEnter, which has already guaranteed
// insert mode before the paste — re-escaping before the trailing Enter would
// drop the prompt back to normal mode and swallow the submit.
func (s *Session) sendEnterRaw() error {
	return s.sendEnterRawToTarget(s.Name)
}

// sendEnterRawToTarget is sendEnterRaw against an explicit tmux target.
func (s *Session) sendEnterRawToTarget(target string) error {
	s.invalidateCache()
	cmd := keySenderExec(s.SocketName, "send-keys", "-t", target, "Enter")
	return runSendKeysBounded(cmd)
}

// SendEnter sends an Enter key to the tmux session. When VimMode is set it
// first guarantees the composer is in insert mode (issue #1264) so the Enter
// submits the message instead of being consumed as a vim normal-mode motion.
// This covers the bare-Enter nudges the send-verify retry loop fires against a
// detected unsent prompt (cmd/agent-deck/session_cmd.go), which would
// otherwise all no-op in normal mode.
func (s *Session) SendEnter() error {
	s.ensureInsertMode()
	return s.sendEnterRaw()
}

// SendEnterChecked checks the pane immediately before every key in the
// optional vim insert sequence and before the final Enter.
func (s *Session) SendEnterChecked(before func() error) error {
	if err := s.ensureInsertModeOnTargetChecked(s.Name, before); err != nil {
		return err
	}
	if err := beforeKeyBatch(before); err != nil {
		return err
	}
	return s.sendEnterRaw()
}

// SendEnterToPrimaryWindow submits input in the managed agent window.
func (s *Session) SendEnterToPrimaryWindow() error {
	target := s.primaryWindowTarget()
	s.ensureInsertModeOnTarget(target)
	return s.sendEnterRawToTarget(target)
}

// OpenKeySender opens a persistent tmux control-mode client bound to this
// session's pane. Used by TUI insert mode (#1102) to amortize the fork+exec
// cost of `tmux send-keys` across a typing burst. Returns nil and an error
// when the user's tmux can't be reached or the session no longer exists;
// callers should fall back to per-call SendKeys / SendEnter / SendNamedKey.
func (s *Session) OpenKeySender() (KeySender, error) {
	return OpenKeySender(s.SocketName, s.Name)
}

// SendNamedKey sends a single tmux named key (e.g. "BSpace", "Up", "Down",
// "Left", "Right", "Tab", "BTab", "C-c", "C-d") to the session. Unlike
// SendKeys it does NOT use the -l flag, so tmux interprets the argument as a
// key name rather than literal text. Used by insert mode (#1094) to forward
// Backspace, arrow keys, Tab, and Ctrl-{C,D} from the TUI to the focused pane.
func (s *Session) SendNamedKey(key string) error {
	s.invalidateCache()
	cmd := keySenderExec(s.SocketName, "send-keys", "-t", s.Name, key)
	return runSendKeysBounded(cmd)
}

// SendNamedKeyToPrimaryWindow sends a tmux key name to the managed agent window.
func (s *Session) SendNamedKeyToPrimaryWindow(key string) error {
	s.invalidateCache()
	cmd := keySenderExec(s.SocketName, "send-keys", "-t", s.primaryWindowTarget(), key)
	return runSendKeysBounded(cmd)
}

// SendKeysAndEnter sends literal text followed by Enter as two separate tmux
// calls with a short delay between them. The delay gives async TUI frameworks
// (Ink/Node.js, curses) time to finish processing the body — in particular a
// bracketed-paste end marker (\e[201~), which `paste-buffer -p` emits for
// multi-line and large payloads — before the Enter arrives in the PTY buffer;
// without it the Enter lands in the same read as the paste-end marker and is
// swallowed. (Measured on tmux 3.7b: `send-keys -l` is NOT wrapped in
// bracketed paste, contrary to what this comment previously claimed — only
// `paste-buffer -p` frames, and only when the pane app has enabled it.)
func (s *Session) SendKeysAndEnter(keys string) error {
	return s.sendKeysAndEnterCheckedToTarget(s.Name, keys, nil, nil, nil)
}

// SendKeysAndEnterToWindow is SendKeysAndEnter aimed at a specific tmux window
// index rather than the session's active window. Quick-approve (#1369) uses it
// to deliver "1"+Enter to the exact window showing a Claude prompt, which is
// often not the active one in a multi-window session.
func (s *Session) SendKeysAndEnterToWindow(windowIndex int, keys string) error {
	return s.sendKeysAndEnterCheckedToTarget(s.windowTarget(windowIndex), keys, nil, nil, nil)
}

// PostPasteCheck inspects the pane immediately after the body has been staged
// into it but BEFORE Enter is pressed, and decides whether it is safe to
// submit. pane is the fresh capture capture() returned (StripANSI not
// applied); captureErr is non-nil when capture() itself failed. Returning
// ok=false withholds the Enter; err, if non-nil, is what
// SendKeysAndEnterChecked returns instead of pressing it.
//
// This is the one hook point that can see the composer after the write and
// before the submit — the only place a caller can catch a paste that landed
// truncated (issue #2079) instead of pressing Enter on a fragment and
// discovering the loss only after the agent has already answered it.
type PostPasteCheck func(pane string, captureErr error) (ok bool, err error)

// SendKeysAndEnterChecked is SendKeysAndEnter with a verification step
// inserted between the paste and the Enter: once the body is staged, capture
// is called and its result handed to check. A false ok withholds Enter
// entirely and SendKeysAndEnterChecked returns check's err (or a generic
// error when err is nil).
//
// check == nil (capture is then never called) reproduces SendKeysAndEnter's
// unconditional behavior exactly, so every existing caller is unaffected.
func (s *Session) SendKeysAndEnterChecked(keys string, capture func() (string, error), check PostPasteCheck) error {
	return s.sendKeysAndEnterCheckedToTarget(s.Name, keys, capture, check, nil)
}

// SendKeysAndEnterCheckedGuarded keeps the ordinary paste check and calls
// before immediately before every tmux keystroke batch, including fallback
// chunks and the final Enter.
func (s *Session) SendKeysAndEnterCheckedGuarded(keys string, capture func() (string, error), check PostPasteCheck, before func() error) error {
	return s.sendKeysAndEnterCheckedToTarget(s.Name, keys, capture, check, before)
}

// sendKeysAndEnterCheckedToTarget is the shared implementation behind
// SendKeysAndEnter, SendKeysAndEnterToWindow and SendKeysAndEnterChecked.
func (s *Session) sendKeysAndEnterCheckedToTarget(target, keys string, capture func() (string, error), check PostPasteCheck, before func() error) error {
	s.invalidateCache()
	// Pin the pane before anything else touches it. A session-name target is
	// re-resolved by tmux on EVERY command, so the probe, the body and the
	// Enter are three independent lookups: if the active window or pane
	// changes between them, agent-deck can inspect the line discipline of
	// pane A, paste the body into pane B and submit it in pane C — a false
	// refusal or an undetected loss, depending which way it drifts. `#{pane_id}`
	// is immutable for the life of the pane, so resolving it once removes the
	// drift for every step that follows. Only done for payloads big enough to
	// need the probe, so ordinary sends pay nothing (issue #1793).
	if len(keys) > canonicalSafeBytes {
		if paneID, err := s.resolvePaneID(target); err == nil && paneID != "" {
			target = paneID
		}
	}
	// Guarantee the composer is in insert mode BEFORE the paste so a vim
	// normal-mode prompt doesn't interpret the message body as motion/command
	// keystrokes (issue #1264). No-op unless VimMode is set.
	if err := s.ensureInsertModeOnTargetChecked(target, before); err != nil {
		return err
	}
	// Use chunked sending for large messages to avoid tmux buffer limits
	if err := s.sendKeysChunkedToTargetChecked(target, keys, before); err != nil {
		return err
	}
	// Delay for TUI apps (Ink, curses) to finish processing bracketed paste
	// before Enter arrives. Without this, tmux 3.2+ paste sequences cause
	// the immediately-following Enter to be swallowed by the paste handler.
	time.Sleep(100 * time.Millisecond)
	if check != nil {
		var pane string
		var capErr error
		if capture != nil {
			pane, capErr = capture()
		} else {
			capErr = fmt.Errorf("SendKeysAndEnterChecked: no capture function provided")
		}
		if ok, err := check(pane, capErr); !ok {
			if err != nil {
				return err
			}
			return fmt.Errorf("prompt delivery check withheld the submit")
		}
	}
	// sendEnterRaw (not SendEnter): we already guaranteed insert mode above and
	// the paste keeps us in insert; re-escaping here would drop back to normal
	// mode and swallow the submit.
	if err := beforeKeyBatch(before); err != nil {
		return err
	}
	return s.sendEnterRawToTarget(target)
}

// SendKeysChunked delivers content to the tmux session's active window.
// Single-line payloads at or below canonicalSafeBytes go out as one
// `send-keys -l`, exactly as before. Multi-line payloads of any size, and
// larger single-line payloads, take the paste transport (load-buffer from
// stdin + paste-buffer -p -r) — the only transport that preserves line
// structure into a TUI composer; see sendKeysChunkedToTarget, pasteToTargetChecked
// and canonical_line.go.
func (s *Session) SendKeysChunked(content string) error {
	return s.sendKeysChunkedToTarget(s.Name, content)
}

func (s *Session) SendKeysChunkedChecked(content string, before func() error) error {
	return s.sendKeysChunkedToTargetChecked(s.Name, content, before)
}

// sendKeysChunkedToTarget is SendKeysChunked against an explicit tmux target.
//
// CR handling: CRLF and bare-CR line breaks are normalized to LF before the
// fork (see the comment in the body), so "multi-line" below means "contains
// any line break", not "contains LF".
//
// The transport fork (issues #1793 + #1855):
//
//   - single-line, ≤ canonicalSafeBytes: one `send-keys -l`. Guaranteed to
//     fit any line discipline, so nothing is inspected and nothing costs
//     extra. This is the overwhelming majority of sends and is byte-for-byte
//     unchanged.
//
//   - multi-line, any size: the paste transport (issue #1855). A body with
//     embedded LFs cannot go out as `send-keys -l`: the bytes arrive intact
//     but UNFRAMED, and to a bracketed-paste TUI composer (Claude Code's
//     Ink prompt) an unframed bare LF is a keystroke, not text — the
//     composer drops it and the lines fuse. Only `paste-buffer -p -r`
//     delivers both properties a multi-line body needs: literal LF (-r,
//     without which tmux rewrites LF to CR, and CR is a submit key) and a
//     bracketed-paste frame (-p) so the composer inserts the newlines as
//     text. Short multi-line bodies skip the line-discipline probe below —
//     every line of a ≤canonicalSafeBytes payload fits any canonical
//     buffer, so there is nothing to refuse.
//
//   - single-line, larger, pane readable and canonical: refuse with
//     *CanonicalOverflowError when the longest line does not fit the pane's
//     canonical buffer. The kernel would discard the overflow AND the
//     submitting Enter, so typing it would leave a half-line in the composer
//     and report a success that never happened. Refusing types nothing.
//
//   - larger, otherwise: deliver via load-buffer + paste-buffer. tmux reads
//     the body from our stdin instead of argv, which removes the ARG_MAX
//     exposure and replaces N paced `send-keys` subprocesses with two calls.
//     If the paste transport is unavailable the chunked send-keys path is
//     still there as a fallback.
//
// The paste transport is NOT what fixes canonical overflow — it was measured
// to fall off the identical cliff (canonical_line.go). Only the refusal, and
// the caller's post-send verification, make the outcome honest.
func (s *Session) sendKeysChunkedToTarget(target, content string) error {
	return s.sendKeysChunkedToTargetChecked(target, content, nil)
}

func (s *Session) sendKeysChunkedToTargetChecked(target, content string, before func() error) error {
	// A CR is a line break to everything downstream of this transport — the
	// tty's ICRNL default turns an incoming CR into NL before the line
	// discipline sees it (the same reason longestMessageLineBytes counts \r
	// as a line terminator) — and to a bracketed-paste composer a CR inside
	// the frame is a submit key. paste-buffer's -r flag only stops tmux's
	// own LF→CR rewrite; it never removes CRs the payload already carries.
	// So normalize here, before the transport fork: a CRLF body (a
	// Windows-authored --message-file) and a bare-CR body both become LF
	// line breaks, which routes them through the framed paste below and
	// keeps the delivered frame CR-free.
	if strings.Contains(content, "\r") {
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
	}
	if len(content) <= canonicalSafeBytes && !strings.Contains(content, "\n") {
		if err := beforeKeyBatch(before); err != nil {
			return err
		}
		return s.sendKeysToTarget(target, content)
	}

	// Above the always-safe size the pane itself decides. An unreadable
	// discipline is "unknown", not "unsafe": deliver and let the caller
	// verify rather than refusing a send that would have worked. Payloads at
	// or below canonicalSafeBytes (multi-line ones reaching here for the
	// paste framing) are safe per line by construction and are never probed
	// nor refused.
	if len(content) > canonicalSafeBytes {
		if ld, err := s.paneLineDiscipline(target); err == nil && ld.Canonical && ld.MaxLine > 0 {
			if longest := longestLineBytes(content); longest > ld.MaxLine-1 {
				return &CanonicalOverflowError{
					LineBytes:  longest,
					LimitBytes: ld.MaxLine,
					TTY:        ld.TTY,
				}
			}
		}
	}

	err := s.pasteToTargetChecked(target, content, before)
	if err == nil {
		return nil
	}
	// Fall back ONLY when the failure happened before any byte could reach the
	// pane. Once `paste-buffer` has been invoked its failure is ambiguous — a
	// timeout can fire after some or all of the body was already written — and
	// re-sending the whole message through send-keys would duplicate or
	// concatenate it in the composer. An ambiguous transport failure is
	// reported, never retried.
	if errors.Is(err, errPasteNotStaged) {
		// Degrading is deliberate — a tmux build whose paste path is down has
		// no framing transport at all, and refusing would fail sends that a
		// cooked-mode consumer would have taken fine. But the degradation must
		// not be silent: for a multi-line body the fallback is the unframed
		// `send-keys -l` transport issue #1855 is about, so the lines can fuse
		// in a bracketed-paste composer exactly as originally reported. Log it
		// so a recurrence of #1855 behind a green exit is attributable to this
		// path instead of looking like the fix regressed.
		statusLog.Warn("paste_transport_unavailable_degraded_to_send_keys",
			slog.String("session", logging.SanitizeValue(s.Name)),
			slog.Bool("multiline", strings.Contains(content, "\n")),
			slog.Int("payload_bytes", len(content)),
			slog.String("error", err.Error()))
		return s.sendKeysChunkedFallbackChecked(target, content, before)
	}
	return err
}

// resolvePaneID returns the immutable `#{pane_id}` (e.g. "%17") currently
// backing target, so subsequent commands address that exact pane rather than
// re-resolving a session name that may have moved.
func (s *Session) resolvePaneID(target string) (string, error) {
	out, err := s.runBoundedOutput("display-message", "-t", target, "-p", "#{pane_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// errPasteNotStaged marks a paste failure that happened BEFORE `paste-buffer`
// ran, so no byte of the payload can have reached the pane and retrying
// through another transport is safe.
var errPasteNotStaged = errors.New("payload was never staged into the pane")

// pasteBufferSeq makes every staged buffer name unique within this process.
var pasteBufferSeq atomic.Uint64

// pasteBufferName returns the tmux buffer name to stage one payload in.
//
// The name MUST be unique per send. tmux buffers are per-SERVER, not per
// session, so a fixed name would be a cross-session data race: two concurrent
// large sends (a conductor fanning messages out to its children is the normal
// case) would have the second `load-buffer` overwrite the first's content
// between its load and its paste, and the first pane would receive the other
// session's message. pid + counter keeps concurrent senders — in this process
// or in a second agent-deck process on the same server — off each other's
// buffers. Named rather than using tmux's numbered stack so the user's own
// copy buffers stay untouched.
func pasteBufferName() string {
	return fmt.Sprintf("agent-deck-send-%d-%d", os.Getpid(), pasteBufferSeq.Add(1))
}

// pasteToTargetChecked delivers content through tmux's paste path: `load-buffer -`
// reads the body from this process's stdin (no argv size limit, no shell
// quoting), then `paste-buffer -p -r -d` writes it to the pane and drops the
// buffer.
//
// The two paste flags are load-bearing for multi-line bodies (issue #1855),
// measured on tmux 3.7b against a raw-mode pane with bracketed paste enabled:
//
//	-r  keeps embedded LFs as LF. Without it tmux rewrites every LF to CR,
//	    and CR is a submit key — a multi-line message gets chopped into N
//	    premature submissions.
//	-p  frames the body in bracketed-paste markers (\e[200~…\e[201~) IF the
//	    pane's application has requested bracketed paste. A TUI composer
//	    only inserts an LF as text inside a paste frame; unframed it is a
//	    keystroke and the lines fuse. Apps that never requested bracketed
//	    paste (a cooked-mode shell) receive the bare body unchanged, so the
//	    flag is safe for every consumer.
//
// before runs ahead of the paste (see beforeKeyBatch); nil skips the check.
func (s *Session) pasteToTargetChecked(target, content string, before func() error) error {
	s.invalidateCache()

	buf := pasteBufferName()
	load := keySenderExec(s.SocketName, "load-buffer", "-b", buf, "-")
	load.Stdin = strings.NewReader(content)
	if err := runSendKeysBounded(load); err != nil {
		// load-buffer can fail after partially creating the buffer (e.g. the
		// deadline fires mid-write), so drop it rather than leaving a
		// half-written payload sitting on the server.
		_ = runSendKeysBounded(keySenderExec(s.SocketName, "delete-buffer", "-b", buf))
		// Nothing was pasted: the payload only ever existed in a server-side
		// buffer, so another transport may safely try again.
		return fmt.Errorf("load-buffer: %v: %w", err, errPasteNotStaged)
	}

	if err := beforeKeyBatch(before); err != nil {
		_ = runSendKeysBounded(keySenderExec(s.SocketName, "delete-buffer", "-b", buf))
		return err
	}
	paste := keySenderExec(s.SocketName, "paste-buffer", "-p", "-r", "-d", "-b", buf, "-t", target)
	if err := runSendKeysBounded(paste); err != nil {
		// -d never ran, so the staged buffer would linger. Drop it before
		// falling back, otherwise the fallback's content and this stale copy
		// both sit in the buffer stack.
		_ = runSendKeysBounded(keySenderExec(s.SocketName, "delete-buffer", "-b", buf))
		// Deliberately NOT errPasteNotStaged: paste-buffer already ran, so an
		// unknown amount of the body may be in the composer. Deleting the
		// buffer does not un-write those bytes.
		return fmt.Errorf("paste-buffer failed after writing to the pane, delivery is indeterminate "+
			"and must not be retried: %w", err)
	}
	return nil
}

// sendKeysChunkedFallbackChecked is the historical paced `send-keys -l`
// transport, kept as the fallback for tmux builds where the paste path fails.
// before runs ahead of every batch (see beforeKeyBatch); nil skips the check.
func (s *Session) sendKeysChunkedFallbackChecked(target, content string, before func() error) error {
	const chunkSize = 4096
	const chunkDelay = 50 * time.Millisecond

	if len(content) <= chunkSize {
		if err := beforeKeyBatch(before); err != nil {
			return err
		}
		return s.sendKeysToTarget(target, content)
	}

	chunks := splitIntoChunks(content, chunkSize)
	for i, chunk := range chunks {
		if err := beforeKeyBatch(before); err != nil {
			return err
		}
		if err := s.sendKeysToTarget(target, chunk); err != nil {
			return fmt.Errorf("failed to send chunk %d/%d: %w", i+1, len(chunks), err)
		}
		if i < len(chunks)-1 {
			time.Sleep(chunkDelay)
		}
	}
	return nil
}

// splitIntoChunks splits content into chunks of at most maxSize bytes,
// preferring to split at newline boundaries. If a single line exceeds maxSize,
// it is split at the byte boundary as a fallback.
func splitIntoChunks(content string, maxSize int) []string {
	if content == "" {
		return nil
	}
	if len(content) <= maxSize {
		return []string{content}
	}

	var chunks []string
	remaining := content

	for len(remaining) > 0 {
		if len(remaining) <= maxSize {
			chunks = append(chunks, remaining)
			break
		}

		// Find the last newline within the chunk boundary
		cutPoint := strings.LastIndex(remaining[:maxSize], "\n")
		if cutPoint > 0 {
			// Include the newline in this chunk
			chunks = append(chunks, remaining[:cutPoint+1])
			remaining = remaining[cutPoint+1:]
		} else {
			// No newline found: hard split at maxSize, backed off to the
			// nearest rune boundary so a multi-byte character is never cut in
			// half across two `send-keys -l` calls (which would put invalid
			// UTF-8 on the wire and corrupt the character in the pane).
			cut := runeSafeCut(remaining, maxSize)
			chunks = append(chunks, remaining[:cut])
			remaining = remaining[cut:]
		}
	}

	return chunks
}

// SendCtrlC sends Ctrl+C (interrupt signal) to the tmux session
func (s *Session) SendCtrlC() error {
	s.invalidateCache()
	cmd := s.tmuxCmd("send-keys", "-t", s.Name, "C-c")
	return runSendKeysBounded(cmd)
}

// SendCtrlU sends Ctrl+U (clear line) to the tmux session
func (s *Session) SendCtrlU() error {
	s.invalidateCache()
	cmd := s.tmuxCmd("send-keys", "-t", s.Name, "C-u")
	return runSendKeysBounded(cmd)
}

// WaitForShellPrompt polls the terminal until a shell prompt is detected
// Returns true if shell prompt found, false if timeout
// Shell prompts: $, #, %, ❯, ➜, or bare > at end of line
func (s *Session) WaitForShellPrompt(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	shellPrompts := []string{"$ ", "# ", "% ", "❯ ", "➜ "}

	for time.Now().Before(deadline) {
		content, err := s.CapturePane()
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		content = StripANSI(content)

		// Get the last non-empty line
		lines := strings.Split(strings.TrimSpace(content), "\n")
		if len(lines) == 0 {
			time.Sleep(pollInterval)
			continue
		}
		lastLine := strings.TrimSpace(lines[len(lines)-1])

		// Check for shell prompts
		for _, prompt := range shellPrompts {
			if strings.HasSuffix(lastLine, strings.TrimSpace(prompt)) ||
				strings.Contains(lastLine, prompt) {
				return true
			}
		}

		// Also check for bare ">" but make sure it's not Claude's input prompt
		// Claude's prompt is just ">" or "> " without path prefix
		// Shell prompts typically have a path or user prefix before >
		if strings.HasSuffix(lastLine, ">") && len(lastLine) > 2 {
			return true
		}

		time.Sleep(pollInterval)
	}

	return false
}

// WaitForReady polls the terminal until the agent is ready for input
// Ready state = NO busy indicator AND prompt visible
// This works for Claude ("> "), Gemini, and other agents
func (s *Session) WaitForReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond
	attempts := 0

	for time.Now().Before(deadline) {
		attempts++
		content, err := s.CapturePane()
		if err != nil {
			statusLog.Debug(
				"wait_for_ready_capture_error",
				slog.Int("attempt", attempts),
				slog.String("error", err.Error()),
			)
			time.Sleep(pollInterval)
			continue
		}
		content = StripANSI(content)

		busy := s.hasBusyIndicator(content)
		prompt := hasPrompt(content)

		if attempts%10 == 0 { // Log every 10th attempt (every second)
			statusLog.Debug(
				"wait_for_ready_status",
				slog.Int("attempt", attempts),
				slog.Bool("busy", busy),
				slog.Bool("prompt", prompt),
			)
		}

		// Check: NOT busy AND has prompt
		if !busy && prompt {
			statusLog.Debug(
				"wait_for_ready_detected",
				slog.Int("attempts", attempts),
				slog.Float64("seconds", float64(attempts)*0.1),
			)
			return true // Ready for input!
		}

		time.Sleep(pollInterval)
	}

	statusLog.Debug("wait_for_ready_timeout", slog.Int("attempts", attempts))
	return false // Timeout
}

// hasPrompt checks for input prompts (Claude, shell, other agents)
func hasPrompt(content string) bool {
	content = StripANSI(content)
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return false
	}

	// Check last 5 lines (Claude's "> " might be above permissions dialog)
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}

	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)

		// Claude prompt: "> " or just ">"
		if strings.Contains(line, "> ") || trimmed == ">" {
			return true
		}

		// Shell prompts: $, #, %, ❯, ➜
		if strings.HasSuffix(trimmed, "$") ||
			strings.HasSuffix(trimmed, "#") ||
			strings.HasSuffix(trimmed, "%") ||
			strings.Contains(line, "❯") ||
			strings.Contains(line, "➜") {
			return true
		}
	}

	return false
}

// IsClaudeRunning checks if Claude appears to be running in the session
// Returns true if Claude indicators are found
func (s *Session) IsClaudeRunning() bool {
	content, err := s.CapturePane()
	if err != nil {
		return false
	}

	// Check for Claude-specific indicators
	claudeIndicators := []string{
		"ctrl+c to interrupt",
		"Thinking...",
		"Connecting...",
		"Press Ctrl-C again to exit",
	}

	// Also check for spinner characters (Claude's busy indicator)
	spinnerChars := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

	for _, indicator := range claudeIndicators {
		if strings.Contains(content, indicator) {
			return true
		}
	}

	// Check last few lines for spinner
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := lines[i]
		for _, c := range spinnerChars {
			if strings.ContainsRune(line, c) {
				return true
			}
		}
	}

	return false
}

// SendCommand sends a command to the tmux session and presses Enter
func (s *Session) SendCommand(command string) error {
	return s.SendKeysAndEnter(command)
}

// ErrInsertHasLineBreak is returned by InsertTextToPrimaryWindow for text
// that carries a CR or LF: either one is a submit key to a shell or to a
// composer without bracketed paste, so it could send the draft.
var ErrInsertHasLineBreak = errors.New("insert text must be a single line (no CR/LF)")

// InsertTextToPrimaryWindow types one line of literal text into the managed
// agent window's composer and never presses Enter: the text stays in the
// composer for the user to edit and submit (session ref, ASK-111). A
// vim-mode composer is first put in insert mode, as session send does.
func (s *Session) InsertTextToPrimaryWindow(text string) error {
	return s.InsertTextToPrimaryWindowChecked(text, nil)
}

// InsertTextToPrimaryWindowChecked is InsertTextToPrimaryWindow with a guard:
// before runs right before every key batch (the vim-mode Escape and i, and
// the text). A non-nil error from it stops the insert there and is returned
// as is, so a guard that refuses before the first batch types nothing.
// session ref uses it to refuse a pane that shows a menu (ASK-111).
func (s *Session) InsertTextToPrimaryWindowChecked(text string, before func() error) error {
	if strings.ContainsAny(text, "\r\n") {
		return ErrInsertHasLineBreak
	}
	if text == "" {
		return nil
	}
	target := s.primaryWindowTarget()
	if err := s.ensureInsertModeOnTargetChecked(target, before); err != nil {
		return err
	}
	if err := beforeKeyBatch(before); err != nil {
		return err
	}
	return s.sendKeysToTarget(target, text)
}
