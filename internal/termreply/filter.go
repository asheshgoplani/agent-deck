package termreply

const (
	escapeByte = 0x1b
	bellByte   = 0x07

	controlSequenceIntroducerByte = '['
	singleShiftThreeByte          = 'O'

	operatingSystemCommandByte    = ']'
	deviceControlStringByte       = 'P'
	applicationProgramCommandByte = '_'
	privacyMessageByte            = '^'
	startOfStringByte             = 'X'

	stringTerminatorByte = '\\'

	csiFinalArrowUpByte       = 'A'
	csiFinalArrowDownByte     = 'B'
	csiFinalArrowRightByte    = 'C'
	csiFinalArrowLeftByte     = 'D'
	csiFinalEndByte           = 'F'
	csiFinalHomeByte          = 'H'
	csiFinalBacktabByte       = 'Z'
	csiFinalTildeByte         = '~'
	csiFinalKittyKeyByte      = 'u'
	csiFinalMouseLegacyByte   = 'M' // X10/legacy mouse report + SGR mouse press
	csiFinalMouseSGRFinalByte = 'm' // SGR mouse release

	// CSI reply finals that tmux needs to see to negotiate terminal
	// capabilities (modifyOtherKeys, extended-keys). Dropping these is what
	// caused #738 (Shift+Enter collapsing to bare CR in iTerm2 default
	// profile): without DA1/DA2 replies, tmux cannot engage the CSI u /
	// modifyOtherKeys protocol with the host terminal.
	csiFinalDeviceAttributesByte = 'c' // DA1 / DA2 reply
	csiFinalDeviceStatusByte     = 'n' // DSR reply
	csiFinalCursorPositionByte   = 'R' // DSR cursor position reply
)

type filterMode uint8

const (
	filterModeIdle filterMode = iota
	filterModeDiscardEscapeString
	filterModeCollectCSI
	filterModeCollectSS3
)

// Filter strips terminal-generated control replies from a byte stream while
// preserving ordinary keyboard input. It is stateful so replies split across
// reads are discarded without relying on terminal-specific payload strings.
type Filter struct {
	mode                filterMode
	pendingEsc          bool
	escapeSeenInDiscard bool
	sequenceBuf         []byte
}

// Active reports whether the filter is carrying parser state across read boundaries.
func (f *Filter) Active() bool {
	return f.pendingEsc || f.mode != filterModeIdle || len(f.sequenceBuf) > 0
}

func isEscapeStringIntroducer(b byte) bool {
	switch b {
	case operatingSystemCommandByte,
		deviceControlStringByte,
		applicationProgramCommandByte,
		privacyMessageByte,
		startOfStringByte:
		return true
	default:
		return false
	}
}

func isSequenceFinalByte(b byte) bool {
	return b >= 0x40 && b <= 0x7e
}

// isKeyboardCSIFinalByte returns true for CSI final bytes that represent
// user input (keyboard or mouse) rather than terminal replies. These are
// preserved even when the filter is armed during the quarantine window.
func isKeyboardCSIFinalByte(b byte) bool {
	switch b {
	case csiFinalArrowUpByte,
		csiFinalArrowDownByte,
		csiFinalArrowRightByte,
		csiFinalArrowLeftByte,
		csiFinalEndByte,
		csiFinalHomeByte,
		csiFinalBacktabByte,
		csiFinalTildeByte,
		csiFinalKittyKeyByte,
		csiFinalMouseLegacyByte,
		csiFinalMouseSGRFinalByte:
		return true
	default:
		return false
	}
}

// isTmuxCapabilityReplyCSIFinalByte returns true for CSI final bytes that
// carry DA/DSR replies tmux needs to negotiate terminal capabilities with the
// host terminal. Per @Clean-Cole's root-cause analysis in #738, swallowing
// these breaks modifyOtherKeys negotiation and collapses Shift+Enter to bare
// CR in iTerm2 default profile. They must pass through to the wrapped tmux
// client regardless of the armed flag. Unlike DCS/OSC/APC/PM/SOS (which are
// purely outer-TUI capability responses and are unconditionally stripped),
// these CSI replies are consumed by tmux itself.
func isTmuxCapabilityReplyCSIFinalByte(b byte) bool {
	switch b {
	case csiFinalDeviceAttributesByte,
		csiFinalDeviceStatusByte,
		csiFinalCursorPositionByte:
		return true
	default:
		return false
	}
}

func flushSequence(out []byte, seq []byte) []byte {
	return append(out, seq...)
}

func (f *Filter) beginSequence(mode filterMode, prefix ...byte) {
	f.mode = mode
	f.sequenceBuf = append(f.sequenceBuf[:0], prefix...)
}

func (f *Filter) resetSequenceState() {
	f.mode = filterModeIdle
	f.sequenceBuf = f.sequenceBuf[:0]
	f.escapeSeenInDiscard = false
}

// Consume filters a chunk of bytes. Escape-string replies (OSC/DCS/APC/PM/SOS)
// are discarded unconditionally — they have no keyboard overlap, so a human
// cannot produce them, and leaking them to the inner PTY has real-world
// failure modes (see #731: iTerm2 XTVERSION DCS leaking as `TERM2 3.6.10n`
// input into the wrapped agent).
//
// CSI sequences are handled by final-byte whitelist:
//
//   - Keyboard/mouse CSIs (arrows, Home/End, backtab, ~ keys, kitty CSI u,
//     mouse M/m) always pass through so user input is never corrupted.
//   - DA/DSR replies (final bytes c/n/R) always pass through so tmux can
//     negotiate modifyOtherKeys with the host terminal (see #738: @Clean-Cole
//     identified that swallowing DA1 collapsed Shift+Enter to bare CR in
//     iTerm2 default profile).
//   - Anything else is gated by armed: discarded during the quarantine
//     window, preserved outside it.
//
// If a reply started in a previous chunk, it continues to be discarded until
// it terminates even if armed is now false.
//
// If final is true, any incomplete pending escape/CSI/SS3 sequence is flushed as
// literal input, while an incomplete discarded escape-string reply is dropped.
func (f *Filter) Consume(src []byte, armed bool, final bool) []byte {
	out := make([]byte, 0, len(src))

	for _, b := range src {
		switch f.mode {
		case filterModeDiscardEscapeString:
			if f.escapeSeenInDiscard {
				f.escapeSeenInDiscard = false
				if b == stringTerminatorByte {
					f.resetSequenceState()
					continue
				}
				if b == escapeByte {
					f.escapeSeenInDiscard = true
				}
				continue
			}

			if b == bellByte {
				f.resetSequenceState()
				continue
			}
			if b == escapeByte {
				f.escapeSeenInDiscard = true
			}
			continue

		case filterModeCollectCSI:
			f.sequenceBuf = append(f.sequenceBuf, b)
			if !isSequenceFinalByte(b) {
				continue
			}

			// DA/DSR replies (final bytes c/n/R) must always pass through so
			// tmux can negotiate modifyOtherKeys with the host terminal (see
			// #738 regression from @Clean-Cole). Keyboard CSIs pass through
			// unconditionally too. Only non-whitelisted CSIs are gated by
			// armed.
			if armed && !isKeyboardCSIFinalByte(b) && !isTmuxCapabilityReplyCSIFinalByte(b) {
				f.resetSequenceState()
				continue
			}

			out = flushSequence(out, f.sequenceBuf)
			f.resetSequenceState()
			continue

		case filterModeCollectSS3:
			f.sequenceBuf = append(f.sequenceBuf, b)
			if !isSequenceFinalByte(b) {
				continue
			}

			out = flushSequence(out, f.sequenceBuf)
			f.resetSequenceState()
			continue
		}

		if f.pendingEsc {
			f.pendingEsc = false
			switch {
			case isEscapeStringIntroducer(b):
				// Escape-string replies (DCS/OSC/APC/PM/SOS) are never
				// legitimate keyboard input — strip regardless of armed
				// state to prevent terminal-response leaks like #731.
				f.mode = filterModeDiscardEscapeString
				continue
			case b == controlSequenceIntroducerByte:
				f.beginSequence(filterModeCollectCSI, escapeByte, controlSequenceIntroducerByte)
				continue
			case b == singleShiftThreeByte:
				f.beginSequence(filterModeCollectSS3, escapeByte, singleShiftThreeByte)
				continue
			case b == escapeByte:
				out = append(out, escapeByte)
				f.pendingEsc = true
				continue
			default:
				out = append(out, escapeByte, b)
				continue
			}
		}

		if b == escapeByte {
			f.pendingEsc = true
			continue
		}

		out = append(out, b)
	}

	if final {
		if f.pendingEsc {
			out = append(out, escapeByte)
		}
		switch f.mode {
		case filterModeCollectCSI, filterModeCollectSS3:
			out = flushSequence(out, f.sequenceBuf)
		case filterModeDiscardEscapeString:
			// Drop incomplete escape-string replies on EOF.
		}
		f.pendingEsc = false
		f.resetSequenceState()
	}

	return out
}
