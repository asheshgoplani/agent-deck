// Package ui provides the Bubble Tea TUI for agent-deck.
// keyboard_compat.go implements compatibility helpers for the Kitty keyboard
// protocol (CSI u encoding) used by Wayland compositors and modern terminals
// such as Ghostty, Foot, and Alacritty.
//
// Background: Bubble Tea v1.3.10 does not parse Kitty keyboard protocol
// sequences. On Wayland, terminals send keys using CSI u encoding by default,
// which causes uppercase shortcuts and uppercase text input to be silently
// dropped. This file provides:
//
//  1. DisableKittyKeyboard / RestoreKittyKeyboard — escape sequences that ask
//     the terminal to fall back to legacy key reporting before the TUI starts.
//
//  2. ParseCSIu — a CSI u sequence parser, available as a public API for
//     future use or for terminals that ignore the protocol-disable request.
//
//  3. NewCSIuReader — a reader that translates CSI u sequences to legacy bytes
//     on the fly, as a belt-and-suspenders fallback.
package ui

import (
	"bytes"
	"io"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/termreply"
	tea "github.com/charmbracelet/bubbletea"
)

// DisableKittyKeyboard writes the escape sequence that pops the Kitty keyboard
// protocol stack, restoring the previous keyboard mode. If nothing was on the
// stack, this is a safe no-op. After this call, Kitty-protocol-aware terminals
// stop sending CSI u sequences and revert to legacy key reporting. Terminals
// that do not support the protocol ignore the sequence.
func DisableKittyKeyboard(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b[<u")
}

// RestoreLegacyKeyboardCmd returns a tea.Cmd that pops the Kitty keyboard
// protocol stack, restoring legacy key reporting on the given writer.
//
// This is the cleanup half of the attach/detach symmetry: when the dashboard
// hands control to tmux via tea.Exec, tmux's extended-keys setting activates
// Kitty/modifyOtherKeys on the outer terminal. Those settings persist after
// the user detaches, so the outer terminal keeps sending CSI u sequences that
// Bubble Tea v1.3.10 cannot parse, silently dropping shifted keys (capitals)
// on the dashboard. Dispatching this command after tea.Exec returns undoes the
// state tmux set.
//
// Takes a writer so tests can substitute a buffer for os.Stdout.
func RestoreLegacyKeyboardCmd(w io.Writer) tea.Cmd {
	return func() tea.Msg {
		DisableKittyKeyboard(w)
		return nil
	}
}

// EnableKittyKeyboard writes the escape sequence that pushes Kitty keyboard
// mode 1 (disambiguate) onto the protocol stack. This re-enables extended key
// reporting so that sequences like Shift+Enter are sent as CSI u codes.
// Call this before attaching to a session that needs Kitty keyboard support
// (e.g. Claude Code). Pair with DisableKittyKeyboard to pop the stack on
// return.
func EnableKittyKeyboard(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b[>1u")
}

// RestoreKittyKeyboard writes the escape sequence that pops the keyboard mode
// stack, restoring the terminal to its previous keyboard mode. Call this when
// the TUI exits so that the terminal returns to normal operation.
func RestoreKittyKeyboard(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b[<u")
}

// ParseCSIu parses a Kitty keyboard protocol (CSI u) escape sequence and
// returns the equivalent tea.KeyMsg. Returns nil if the data is not a valid
// CSI u sequence.
//
// The CSI u format is:  ESC '[' <codepoint> [';' <modifier>] 'u'
//
// Modifier encoding (1 + bitmask):
//
//	1 = no modifier
//	2 = shift      (1 + 1)
//	3 = alt        (1 + 2)
//	4 = shift+alt  (1 + 1 + 2)
//	5 = ctrl       (1 + 4)
//	6 = shift+ctrl (1 + 1 + 4)
func ParseCSIu(data []byte) *tea.KeyMsg {
	// Minimum sequence: ESC [ <digit> u  (4 bytes)
	if len(data) < 4 {
		return nil
	}
	if data[0] != 0x1b || data[1] != '[' {
		return nil
	}
	// Must end with 'u'
	if data[len(data)-1] != 'u' {
		return nil
	}

	// Parse the interior: <codepoint> or <codepoint>;<modifier>
	interior := data[2 : len(data)-1]
	semicolon := bytes.IndexByte(interior, ';')

	var codepoint int
	modifier := 1 // default: no modifier

	if semicolon < 0 {
		// No modifier section
		codepoint = parseDecimalBytes(interior)
		if codepoint < 0 {
			return nil
		}
	} else {
		codepoint = parseDecimalBytes(interior[:semicolon])
		if codepoint < 0 {
			return nil
		}
		modifier = parseDecimalBytes(interior[semicolon+1:])
		if modifier < 1 {
			return nil
		}
	}

	// Decode modifier bitmask (modifier = 1 + bitmask)
	bitmask := modifier - 1
	shiftHeld := (bitmask & 0x01) != 0
	// altHeld   := (bitmask & 0x02) != 0  // reserved for future use
	ctrlHeld := (bitmask & 0x04) != 0

	// Map well-known control codepoints to tea key types.
	switch codepoint {
	case 13: // CR = Enter
		msg := tea.KeyMsg{Type: tea.KeyEnter}
		return &msg
	case 9: // HT = Tab
		msg := tea.KeyMsg{Type: tea.KeyTab}
		return &msg
	case 27: // ESC
		msg := tea.KeyMsg{Type: tea.KeyEsc}
		return &msg
	case 127: // DEL = Backspace
		msg := tea.KeyMsg{Type: tea.KeyBackspace}
		return &msg
	case 32: // Space
		msg := tea.KeyMsg{Type: tea.KeySpace}
		return &msg
	}

	// Ctrl-modified regular keys: Ctrl+a = 0x01, Ctrl+b = 0x02, …
	if ctrlHeld && codepoint >= 97 && codepoint <= 122 {
		// 'a'=97 -> ctrl sequence 1, 'b'=98 -> 2, …
		ctrlRune := rune(codepoint - 96)
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ctrlRune}}
		return &msg
	}

	// Regular rune: apply shift to lowercase letters.
	r := rune(codepoint)
	if shiftHeld && r >= 'a' && r <= 'z' {
		r = r - 'a' + 'A'
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
	return &msg
}

// ParseModifyOtherKeys parses an xterm modifyOtherKeys escape sequence and
// returns the equivalent tea.KeyMsg. Returns nil if the data is not a valid
// modifyOtherKeys sequence.
//
// The modifyOtherKeys format is:  ESC '[' '27' ';' <modifier> ';' <codepoint> '~'
//
// tmux with extended-keys sends this format. The modifier encoding is the same
// as CSI u (1 + bitmask).
func ParseModifyOtherKeys(data []byte) *tea.KeyMsg {
	// Minimum: ESC [ 2 7 ; <mod> ; <code> ~  (9 bytes)
	if len(data) < 9 {
		return nil
	}
	if data[0] != 0x1b || data[1] != '[' {
		return nil
	}
	if data[len(data)-1] != '~' {
		return nil
	}

	// Interior between '[' and '~': must be "27;<modifier>;<codepoint>"
	interior := data[2 : len(data)-1]

	// Split on semicolons: expect exactly ["27", modifier, codepoint]
	parts := bytes.Split(interior, []byte{';'})
	if len(parts) != 3 {
		return nil
	}
	prefix := parseDecimalBytes(parts[0])
	if prefix != 27 {
		return nil
	}
	modifier := parseDecimalBytes(parts[1])
	if modifier < 1 {
		return nil
	}
	codepoint := parseDecimalBytes(parts[2])
	if codepoint < 0 {
		return nil
	}

	// Reuse the same modifier logic as ParseCSIu
	bitmask := modifier - 1
	shiftHeld := (bitmask & 0x01) != 0
	ctrlHeld := (bitmask & 0x04) != 0

	switch codepoint {
	case 13:
		msg := tea.KeyMsg{Type: tea.KeyEnter}
		return &msg
	case 9:
		msg := tea.KeyMsg{Type: tea.KeyTab}
		return &msg
	case 27:
		msg := tea.KeyMsg{Type: tea.KeyEsc}
		return &msg
	case 127:
		msg := tea.KeyMsg{Type: tea.KeyBackspace}
		return &msg
	case 32:
		msg := tea.KeyMsg{Type: tea.KeySpace}
		return &msg
	}

	if ctrlHeld && codepoint >= 97 && codepoint <= 122 {
		ctrlRune := rune(codepoint - 96)
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ctrlRune}}
		return &msg
	}

	r := rune(codepoint)
	if shiftHeld && r >= 'a' && r <= 'z' {
		r = r - 'a' + 'A'
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
	return &msg
}

// parseDecimalBytes parses a decimal integer from a byte slice.
// Returns -1 if the slice is empty or contains non-digit characters.
func parseDecimalBytes(b []byte) int {
	if len(b) == 0 {
		return -1
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// csiuReader is an io.Reader that intercepts Kitty keyboard protocol (CSI u)
// sequences in the byte stream and translates them into legacy byte sequences
// that Bubble Tea can parse. All other bytes pass through unchanged.
type csiuReader struct {
	src         io.Reader
	outBuf      []byte // pending translated bytes to emit
	inBuf       []byte // buffered input bytes not yet processed
	err         error  // pending source error to return after draining buffers
	replyFilter termreply.Filter
}

// csiuFileReader wraps a *os.File and overrides Read with CSI u translation.
// It preserves the *os.File interface (including Fd()) so that Bubble Tea can
// still call terminal.MakeRaw on stdin to enter raw mode.
//
// This is required because tea.WithInput(plainIoReader) loses the *os.File
// type assertion, which Bubble Tea needs to set the terminal to raw mode.
// Without raw mode, arrow keys and other escape sequences appear as visible
// text instead of being interpreted.
type csiuFileReader struct {
	*os.File
	inner *csiuReader
}

// Read implements io.Reader using the CSI u translation layer.
func (r *csiuFileReader) Read(p []byte) (int, error) {
	return r.inner.Read(p)
}

// NewCSIuReader returns a reader that wraps r and translates any CSI u
// sequences to their legacy equivalents. This is a belt-and-suspenders
// fallback for terminals that do not honor DisableKittyKeyboard.
//
// If r is a *os.File, the returned reader also implements the *os.File
// interface (preserving Fd() for terminal raw-mode setup by Bubble Tea).
// If r is any other io.Reader, a plain io.Reader is returned.
func NewCSIuReader(r io.Reader) io.Reader {
	inner := &csiuReader{
		src:   r,
		inBuf: make([]byte, 0, 256),
	}
	if f, ok := r.(*os.File); ok {
		return &csiuFileReader{File: f, inner: inner}
	}
	return inner
}

// Read implements io.Reader. It reads from the underlying source, translates
// CSI u sequences, and returns the result.
func (c *csiuReader) Read(p []byte) (int, error) {
	// Drain any previously translated bytes first.
	if len(c.outBuf) > 0 {
		n := copy(p, c.outBuf)
		c.outBuf = c.outBuf[n:]
		return n, nil
	}

	for {
		if c.err != nil {
			return 0, c.err
		}

		// Read new bytes from the source into the internal buffer.
		tmp := make([]byte, len(p))
		n, err := c.src.Read(tmp)
		if n > 0 {
			chunk := tmp[:n]
			// Always run the reply filter. Escape-string families (DCS/OSC/
			// APC/PM/SOS) are never keyboard input and can arrive outside
			// any explicit quarantine window (e.g. iTerm2 XTVERSION reply on
			// focus/resize — #731). `armed` stays tied to termreply.Active()
			// so generic CSI pass-through works for keyboard input.
			chunk = c.replyFilter.Consume(chunk, termreply.Active(), false)
			c.inBuf = append(c.inBuf, chunk...)
		}
		if err == io.EOF {
			c.inBuf = append(c.inBuf, c.replyFilter.Consume(nil, termreply.Active(), true)...)
		}

		processed := c.translate(err == io.EOF)
		if len(processed) > 0 {
			copied := copy(p, processed)
			if copied < len(processed) {
				c.outBuf = append(c.outBuf, processed[copied:]...)
			}
			if err != nil {
				c.err = err
			}
			return copied, nil
		}

		if err != nil {
			c.err = err
			return 0, err
		}
	}
}

// translate scans c.inBuf for CSI u / modifyOtherKeys sequences and replaces
// complete matches with their legacy byte representation. Incomplete ESC[
// sequences are left buffered for the next Read call.
func (c *csiuReader) translate(final bool) []byte {
	if len(c.inBuf) == 0 {
		return nil
	}

	out := make([]byte, 0, len(c.inBuf))
	i := 0
	for i < len(c.inBuf) {
		// Look for ESC '[' to start a potential CSI sequence.
		if c.inBuf[i] != 0x1b {
			out = append(out, c.inBuf[i])
			i++
			continue
		}

		// Preserve a lone ESC as-is to avoid hanging standalone escape.
		if i+1 >= len(c.inBuf) {
			out = append(out, c.inBuf[i])
			i++
			continue
		}
		if c.inBuf[i+1] != '[' {
			out = append(out, c.inBuf[i])
			i++
			continue
		}

		// We have ESC '['. Scan forward for the CSI final byte.
		// CSI final bytes are in the range 0x40-0x7E (@ through ~).
		// Only 'u' and '~' get special handling; everything else passes through.
		j := i + 2
		for j < len(c.inBuf) && (c.inBuf[j] < 0x40 || c.inBuf[j] > 0x7E) {
			j++
		}

		if j >= len(c.inBuf) {
			if final {
				out = append(out, c.inBuf[i:]...)
				i = len(c.inBuf)
			}
			break
		}

		seq := c.inBuf[i : j+1]
		if c.inBuf[j] != 'u' {
			// Check for modifyOtherKeys format: ESC[27;modifier;codepoint~
			if c.inBuf[j] == '~' {
				if msg := ParseModifyOtherKeys(seq); msg != nil {
					switch msg.Type {
					case tea.KeyEnter:
						out = append(out, '\r')
					case tea.KeyTab:
						out = append(out, '\t')
					case tea.KeyEsc:
						out = append(out, 0x1b)
					case tea.KeyBackspace:
						out = append(out, 127)
					case tea.KeySpace:
						out = append(out, ' ')
					case tea.KeyRunes:
						for _, r := range msg.Runes {
							out = append(out, []byte(string(r))...)
						}
					default:
						out = append(out, seq...)
					}
					i = j + 1
					continue
				}
			}
			// Not a CSI u or modifyOtherKeys sequence — pass through as-is
			out = append(out, seq...)
			i = j + 1
			continue
		}

		// Potential CSI u sequence: c.inBuf[i..j] inclusive
		msg := ParseCSIu(seq)
		if msg == nil {
			// Not a valid CSI u, pass through
			out = append(out, seq...)
			i = j + 1
			continue
		}

		// Translate to legacy bytes
		switch msg.Type {
		case tea.KeyEnter:
			out = append(out, '\r')
		case tea.KeyTab:
			out = append(out, '\t')
		case tea.KeyEsc:
			out = append(out, 0x1b)
		case tea.KeyBackspace:
			out = append(out, 127)
		case tea.KeySpace:
			out = append(out, ' ')
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				out = append(out, []byte(string(r))...)
			}
		default:
			// Unknown mapped type, pass original sequence through
			out = append(out, seq...)
		}

		i = j + 1
	}

	c.inBuf = append(c.inBuf[:0], c.inBuf[i:]...)
	return out
}
