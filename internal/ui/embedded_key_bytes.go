package ui

import tea "github.com/charmbracelet/bubbletea"

// legacyKeySequences re-encodes the named keys Bubble Tea parses out of the
// terminal stream. Only the plain xterm forms are needed: these bytes replay
// keystrokes typed during the embedded connect interval into a tmux client,
// and tmux reads the legacy encodings from any TERM.
var legacyKeySequences = map[tea.KeyType]string{
	tea.KeyUp:       "\x1b[A",
	tea.KeyDown:     "\x1b[B",
	tea.KeyRight:    "\x1b[C",
	tea.KeyLeft:     "\x1b[D",
	tea.KeyHome:     "\x1b[H",
	tea.KeyEnd:      "\x1b[F",
	tea.KeyInsert:   "\x1b[2~",
	tea.KeyDelete:   "\x1b[3~",
	tea.KeyPgUp:     "\x1b[5~",
	tea.KeyPgDown:   "\x1b[6~",
	tea.KeyShiftTab: "\x1b[Z",
}

// keyMsgRawBytes turns a key event back into the bytes a terminal would have
// sent for it. Bubble Tea parses stdin before Home can switch the input router
// into session mode, so a keystroke that shares a read with Enter, or lands
// while the PTY is still connecting, reaches Home as a KeyMsg the router never
// saw in raw form. Returning ok=false means the key has no faithful legacy
// encoding (function keys, modified arrows); those are dropped as before.
func keyMsgRawBytes(msg tea.KeyMsg) ([]byte, bool) {
	var out []byte
	if msg.Alt {
		out = append(out, 0x1b)
	}
	switch {
	case msg.Type == tea.KeyRunes:
		text := string(msg.Runes)
		if msg.Paste {
			text = bracketedPasteStart + text + bracketedPasteEnd
		}
		out = append(out, text...)
	case msg.Type == tea.KeySpace:
		out = append(out, ' ')
	case msg.Type >= 0 && msg.Type <= 0x7f:
		// Control bytes carry their own code: Enter is \r, Tab \t, Escape
		// 0x1b, Backspace 0x7f, Ctrl+A..Z 0x01..0x1a.
		out = append(out, byte(msg.Type))
	default:
		seq, ok := legacyKeySequences[msg.Type]
		if !ok {
			return nil, false
		}
		out = append(out, seq...)
	}
	return out, true
}
