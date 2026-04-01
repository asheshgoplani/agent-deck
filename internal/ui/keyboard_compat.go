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
	src    io.Reader
	outBuf []byte // pending translated bytes to emit
	inBuf  []byte // buffered input bytes not yet processed
}

// NewCSIuReader returns a reader that wraps r and translates any CSI u
// sequences to their legacy equivalents. This is a belt-and-suspenders
// fallback for terminals that do not honor DisableKittyKeyboard.
func NewCSIuReader(r io.Reader) io.Reader {
	return &csiuReader{
		src:   r,
		inBuf: make([]byte, 0, 256),
	}
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

	// Read new bytes from the source into the internal buffer.
	tmp := make([]byte, len(p))
	n, err := c.src.Read(tmp)
	if n > 0 {
		c.inBuf = append(c.inBuf, tmp[:n]...)
	}

	// Process everything currently in inBuf.
	processed := c.translate(c.inBuf)
	c.inBuf = c.inBuf[:0]

	if len(processed) == 0 {
		return 0, err
	}

	copied := copy(p, processed)
	if copied < len(processed) {
		c.outBuf = append(c.outBuf, processed[copied:]...)
	}
	return copied, err
}

// translate scans buf for CSI u sequences and replaces them with the
// legacy byte representation of the corresponding key. All other bytes are
// returned unchanged.
func (c *csiuReader) translate(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}

	var out []byte
	i := 0
	for i < len(buf) {
		// Look for ESC '[' to start a potential CSI sequence.
		if buf[i] != 0x1b || i+2 >= len(buf) || buf[i+1] != '[' {
			if out == nil {
				out = buf[:0:0] // start building output lazily
			}
			out = append(out, buf[i])
			i++
			continue
		}

		// We have ESC '['. Scan forward for the terminator.
		j := i + 2
		for j < len(buf) && buf[j] != 'u' && buf[j] != 'A' && buf[j] != 'B' &&
			buf[j] != 'C' && buf[j] != 'D' && buf[j] != 'H' && buf[j] != 'F' &&
			buf[j] != '~' {
			j++
		}

		if j >= len(buf) {
			// Incomplete sequence — pass ESC through and continue
			if out == nil {
				out = buf[:0:0]
			}
			out = append(out, buf[i])
			i++
			continue
		}

		if buf[j] != 'u' {
			// Not a CSI u sequence — pass through as-is
			if out == nil {
				out = buf[:0:0]
			}
			out = append(out, buf[i:j+1]...)
			i = j + 1
			continue
		}

		// Potential CSI u sequence: buf[i..j] inclusive
		seq := buf[i : j+1]
		msg := ParseCSIu(seq)
		if msg == nil {
			// Not a valid CSI u, pass through
			if out == nil {
				out = buf[:0:0]
			}
			out = append(out, seq...)
			i = j + 1
			continue
		}

		if out == nil {
			out = buf[:0:0]
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

	if out == nil {
		// No CSI u sequences found — return original buffer directly
		return buf
	}
	return out
}
