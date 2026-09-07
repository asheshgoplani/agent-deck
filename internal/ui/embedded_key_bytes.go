package ui

import tea "github.com/charmbracelet/bubbletea"

// legacyKeySequences re-encodes every named key Bubble Tea parses out of the
// terminal stream, in the xterm form tmux reads from any TERM. These bytes
// replay keystrokes typed during the embedded connect interval into the tmux
// client; TestKeyMsgRawBytesCoversEveryNamedKey fails when Bubble Tea grows a
// named key this table lacks.
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

	// xterm modifier parameters: 2 = Shift, 5 = Ctrl, 6 = Ctrl+Shift.
	tea.KeyShiftUp:        "\x1b[1;2A",
	tea.KeyShiftDown:      "\x1b[1;2B",
	tea.KeyShiftRight:     "\x1b[1;2C",
	tea.KeyShiftLeft:      "\x1b[1;2D",
	tea.KeyShiftHome:      "\x1b[1;2H",
	tea.KeyShiftEnd:       "\x1b[1;2F",
	tea.KeyCtrlUp:         "\x1b[1;5A",
	tea.KeyCtrlDown:       "\x1b[1;5B",
	tea.KeyCtrlRight:      "\x1b[1;5C",
	tea.KeyCtrlLeft:       "\x1b[1;5D",
	tea.KeyCtrlHome:       "\x1b[1;5H",
	tea.KeyCtrlEnd:        "\x1b[1;5F",
	tea.KeyCtrlPgUp:       "\x1b[5;5~",
	tea.KeyCtrlPgDown:     "\x1b[6;5~",
	tea.KeyCtrlShiftUp:    "\x1b[1;6A",
	tea.KeyCtrlShiftDown:  "\x1b[1;6B",
	tea.KeyCtrlShiftRight: "\x1b[1;6C",
	tea.KeyCtrlShiftLeft:  "\x1b[1;6D",
	tea.KeyCtrlShiftHome:  "\x1b[1;6H",
	tea.KeyCtrlShiftEnd:   "\x1b[1;6F",

	// Function keys: SS3 for F1-F4 as vt100/xterm send them, CSI ~ above.
	tea.KeyF1:  "\x1bOP",
	tea.KeyF2:  "\x1bOQ",
	tea.KeyF3:  "\x1bOR",
	tea.KeyF4:  "\x1bOS",
	tea.KeyF5:  "\x1b[15~",
	tea.KeyF6:  "\x1b[17~",
	tea.KeyF7:  "\x1b[18~",
	tea.KeyF8:  "\x1b[19~",
	tea.KeyF9:  "\x1b[20~",
	tea.KeyF10: "\x1b[21~",
	tea.KeyF11: "\x1b[23~",
	tea.KeyF12: "\x1b[24~",
	tea.KeyF13: "\x1b[25~",
	tea.KeyF14: "\x1b[26~",
	tea.KeyF15: "\x1b[28~",
	tea.KeyF16: "\x1b[29~",
	tea.KeyF17: "\x1b[31~",
	tea.KeyF18: "\x1b[32~",
	tea.KeyF19: "\x1b[33~",
	tea.KeyF20: "\x1b[34~",
}

// keyMsgRawBytes turns a key event back into the bytes a terminal would have
// sent for it. Bubble Tea parses stdin before Home can switch the input router
// into session mode, so a keystroke that shares a read with Enter, or lands
// while the PTY is still connecting, reaches Home as a KeyMsg the router never
// saw in raw form. Alt is re-encoded as the ESC prefix tmux reads as Meta.
// ok=false is reserved for a key type this package does not know, which the
// coverage test keeps empty.
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
