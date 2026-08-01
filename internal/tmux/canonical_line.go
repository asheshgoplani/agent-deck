package tmux

import (
	"errors"
	"fmt"
	"strings"
)

// Canonical-mode line buffering: why a big `session send` can vanish.
//
// A pane's tty has a line discipline. When the program reading the pane runs
// in CANONICAL mode (ICANON set — a plain shell command like `cat`, a
// `read` loop, any non-TUI reader), the kernel buffers input until a line
// terminator arrives and only then hands the line to the reader. That buffer
// is finite and it is PER LINE, not per write. Once a line exceeds the
// buffer with no terminator in sight, the kernel throws input away — and the
// Enter that would have submitted it is thrown away with it. The send
// "succeeds" at every layer that can see it while nothing is delivered.
//
// Measured on the transports agent-deck can choose between (macOS 15.5,
// tmux 3.7b, reader = `cat > file`, payload = N bytes of ASCII then Enter):
//
//	payload   send-keys -l   load-buffer+paste-buffer
//	1000      1001 bytes     1001 bytes
//	1023      1024 bytes     1024 bytes
//	1024         0 bytes        0 bytes
//	4095         0 bytes        0 bytes
//
// The cliff sits at 1024 bytes INCLUDING the terminator, and both transports
// fall off it at exactly the same place. That is the finding that decides the
// shape of this fix: the limit belongs to the READER's line discipline, so no
// choice of writer — not chunked send-keys, not load-buffer/paste-buffer, not
// bracketed paste — can move it. Issue #1793 assumed a 4096-byte boundary and
// PR #1819 tried to fix it by shrinking send-keys chunks; neither matches what
// the terminal actually does.
//
// Against the same panes with the reader in RAW mode (ICANON clear — every
// TUI agent: Claude Code, Codex, a readline shell prompt) all three transports
// delivered 8000 bytes intact. So the failure is not "big sends are broken",
// it is "big sends into a canonical-mode reader are impossible", and the only
// honest handling is to detect that state and refuse rather than to type half
// a line into a buffer that will discard it.

const (
	// canonicalSafeBytes is the largest payload guaranteed to survive any
	// pane's line discipline without inspecting it. 1023 = the smallest
	// canonical line buffer agent-deck runs against (macOS MAX_CANON is
	// 1024 and counts the line terminator; measured cliff above), minus the
	// one byte the trailing Enter occupies.
	//
	// This is a "no questions asked" size, not the real limit: payloads
	// above it are not assumed to fail, they are checked against the actual
	// pane (see paneLineDiscipline).
	canonicalSafeBytes = 1023

	// canonMinDarwin is MAX_CANON on darwin (sys/syslimits.h: MAX_CANON 1024,
	// enforced by the tty line discipline in bsd/kern/tty.c) and matches the
	// measured cliff above.
	canonMinDarwin = 1024

	// canonMinLinux is the usable n_tty canonical buffer on Linux. The buffer
	// is N_TTY_BUF_SIZE = 4096 (drivers/tty/n_tty.c) but n_tty_receive_char
	// discards input once read_cnt reaches N_TTY_BUF_SIZE-1, so 4095 bytes
	// including the terminator is what actually survives — which is why the
	// 4095-byte payload in issue #1793 was lost on Linux while a 4094-byte
	// one would not have been.
	//
	// This constant is deliberately used in preference to
	// fpathconf(_PC_MAX_CANON), which reports the POSIX minimum of 255 on
	// Linux while the kernel really buffers 4095; trusting fpathconf there
	// would reject ordinary 300-byte sends.
	canonMinLinux = 4095
)

// ErrCanonicalLineOverflow reports that a payload cannot be delivered to the
// target pane because the pane's reader is in canonical mode and one of the
// payload's lines is longer than that pane's canonical line buffer. Callers
// must surface this as a delivery FAILURE: nothing was typed, so nothing was
// corrupted, and retrying the same body against the same pane will fail the
// same way.
var ErrCanonicalLineOverflow = errors.New("payload line exceeds the pane's canonical-mode line buffer")

// CanonicalOverflowError carries the measured numbers behind
// ErrCanonicalLineOverflow so the CLI can report them instead of a guess.
type CanonicalOverflowError struct {
	// LineBytes is the length of the longest line in the payload.
	LineBytes int
	// LimitBytes is the pane's canonical line buffer, terminator included.
	LimitBytes int
	// TTY is the pane's tty path, as reported by tmux.
	TTY string
}

func (e *CanonicalOverflowError) Error() string {
	return fmt.Sprintf(
		"%v: longest payload line is %d bytes but %s is in canonical mode with a %d-byte line buffer "+
			"(terminator included), so the kernel would discard the overflow and the submitting Enter with it; "+
			"nothing was typed",
		ErrCanonicalLineOverflow, e.LineBytes, e.TTY, e.LimitBytes)
}

func (e *CanonicalOverflowError) Unwrap() error { return ErrCanonicalLineOverflow }

// lineDiscipline is the observed input mode of a pane's tty.
type lineDiscipline struct {
	// Canonical is true when the reader has ICANON set, i.e. the kernel is
	// buffering input per line instead of delivering it byte by byte.
	Canonical bool
	// MaxLine is the canonical line buffer in bytes, terminator included.
	// Only meaningful when Canonical is true.
	MaxLine int
	// TTY is the pane's tty device path.
	TTY string
}

// paneLineDiscipline reads the line discipline of target's tty. It costs one
// `tmux display-message` plus one open+tcgetattr, so callers must only reach
// for it when a payload is actually large enough to care (> canonicalSafeBytes).
// An error means "could not tell" — never "safe" and never "unsafe"; callers
// treat an unreadable discipline as unknown and fall through to delivery plus
// verification rather than refusing.
func (s *Session) paneLineDiscipline(target string) (lineDiscipline, error) {
	out, err := s.runBoundedOutput("display-message", "-t", target, "-p", "#{pane_tty}")
	if err != nil {
		return lineDiscipline{}, fmt.Errorf("read pane tty: %w", err)
	}
	tty := strings.TrimSpace(string(out))
	if tty == "" {
		return lineDiscipline{}, errors.New("read pane tty: tmux reported no tty for the pane")
	}
	ld, err := ttyLineDiscipline(tty)
	if err != nil {
		return lineDiscipline{}, err
	}
	ld.TTY = tty
	return ld, nil
}

// longestLineBytes returns the length in bytes of the longest \n-delimited
// line in content. Canonical buffering is per line, so this — not len(content)
// — is the quantity that has to fit.
func longestLineBytes(content string) int {
	longest := 0
	for {
		i := strings.IndexByte(content, '\n')
		if i < 0 {
			if len(content) > longest {
				longest = len(content)
			}
			return longest
		}
		if i > longest {
			longest = i
		}
		content = content[i+1:]
	}
}

// runeSafeCut returns the largest cut point ≤ maxSize that does not land in
// the middle of a UTF-8 sequence. A rune is at most 4 bytes, so backing off
// past 4 continuation bytes is impossible; if it happens the input was not
// valid UTF-8 to begin with and the raw maxSize cut is returned unchanged.
func runeSafeCut(s string, maxSize int) int {
	if maxSize >= len(s) {
		return len(s)
	}
	cut := maxSize
	for i := 0; i < 3 && cut > 0; i++ {
		// 0b10xxxxxx marks a UTF-8 continuation byte: cutting here would
		// split the rune that started earlier.
		if s[cut]&0xC0 != 0x80 {
			return cut
		}
		cut--
	}
	if cut > 0 && s[cut]&0xC0 != 0x80 {
		return cut
	}
	return maxSize
}
