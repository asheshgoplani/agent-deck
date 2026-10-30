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

	colorReplyMaxLen = 64 // tmux reads a color reply with a 127+ byte payload as keys

	// heldInputMaxLen bounds the bytes held for KeysOnIdle. Terminal replies
	// are far shorter; anything longer is typing and is released as input.
	heldInputMaxLen = 4096
)

var colorReplyPrefixes = [...]string{"\x1b]10;", "\x1b]11;"}

type filterMode uint8

const (
	filterModeIdle filterMode = iota
	filterModeDiscardEscapeString
	filterModeCollectCSI
	filterModeCollectSS3
	filterModeCollectColorReply
)

// Filter strips terminal-generated control replies from a byte stream while
// preserving ordinary keyboard input. It is stateful so replies split across
// reads are discarded without relying on terminal-specific payload strings.
type Filter struct {
	// ForwardColorReplies passes OSC 10/11 color replies through. Set it only
	// for a tmux 3.4+ client: it queries the host terminal's colors, consumes
	// every reply, and answers its panes' OSC 11 queries from them.
	ForwardColorReplies bool

	// KeysOnIdle makes an escape string wait for its terminator held instead
	// of being dropped byte by byte, so FlushIdle can end it. A terminal
	// writes a reply in one piece, so one still unterminated when the caller's
	// hold window has passed was typed: with Option-as-Meta, Alt+Shift+- is
	// ESC _ (#2931). Begun outside the quarantine, FlushIdle returns it as
	// input; begun inside, where replies are expected and must not be typed,
	// FlushIdle drops it. Either way the filter goes back to idle. Without
	// this, the filter discards every later key, the detach key included,
	// waiting for a terminator that typing never produces. Set it only when
	// the caller calls FlushIdle once its hold window has passed, as the
	// attach stdin pump does.
	KeysOnIdle bool

	mode                filterMode
	pendingEsc          bool
	escapeSeenInDiscard bool
	sequenceBuf         []byte

	// held is the unterminated escape string kept for FlushIdle (KeysOnIdle).
	holding   bool
	heldArmed bool // begun inside the quarantine: dropped, not released
	held      []byte

	// usedReplyKind tracks, per reply category, whether a whitelisted DA/DSR
	// reply has already been passed through to tmux while armed. See
	// replyKind and the budget check in Consume.
	usedReplyKind [replyKindCount]bool
}

// replyKind classifies a whitelisted DA/DSR CSI reply so at most one of each
// kind is forwarded per quarantine window while armed (see Consume).
type replyKind int

const (
	replyKindDA1 replyKind = iota // CSI ? ... c  (DA1 primary device attributes)
	replyKindDA2                  // CSI > ... c  (DA2 secondary device attributes)
	replyKindDSR                  // CSI ... n (device status report)
	replyKindCPR                  // CSI ... R (cursor position report)
	replyKindCount
)

// classifyReply returns the replyKind for a completed CSI sequence whose
// final byte is a whitelisted DA/DSR reply byte. seq is the full sequence
// including the leading ESC and '[' bytes.
func classifyReply(seq []byte, final byte) replyKind {
	if final == csiFinalDeviceStatusByte {
		return replyKindDSR
	}
	if final == csiFinalCursorPositionByte {
		return replyKindCPR
	}
	if len(seq) > 2 && seq[2] == '>' {
		return replyKindDA2
	}
	return replyKindDA1
}

// ResetReplyBudget starts a new quarantine window without discarding parser
// state for a reply split across reads.
func (f *Filter) ResetReplyBudget() {
	f.usedReplyKind = [replyKindCount]bool{}
}

// Holding reports whether an unterminated escape string is held for FlushIdle.
func (f *Filter) Holding() bool {
	return f.holding
}

// FlushIdle ends the held escape string once the caller's hold window has
// passed without a terminator, and returns it as input. A string begun inside
// the quarantine is dropped instead and FlushIdle returns nil, as it does when
// nothing is held. See KeysOnIdle.
func (f *Filter) FlushIdle() []byte {
	if !f.holding {
		return nil
	}
	var out []byte
	if !f.heldArmed {
		out = append(out, f.held...)
	}
	f.resetSequenceState()
	return out
}

// hold records b as part of the held escape string. Past heldInputMaxLen it
// cannot be a reply, so the string is ended as FlushIdle would end it.
func (f *Filter) hold(out []byte, b byte) []byte {
	if !f.holding {
		return out
	}
	f.held = append(f.held, b)
	if len(f.held) <= heldInputMaxLen {
		return out
	}
	return append(out, f.FlushIdle()...)
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

func mayBeColorReply(seq []byte) bool {
	for _, prefix := range colorReplyPrefixes {
		n := min(len(seq), len(prefix))
		if string(seq[:n]) == prefix[:n] {
			return true
		}
	}
	return false
}

func isEscapeStringTerminated(seq []byte) bool {
	n := len(seq)
	return seq[n-1] == bellByte || (n >= 2 && seq[n-2] == escapeByte && seq[n-1] == stringTerminatorByte)
}

func flushSequence(out []byte, seq []byte) []byte {
	return append(out, seq...)
}

func (f *Filter) beginSequence(mode filterMode, prefix ...byte) {
	f.mode = mode
	f.sequenceBuf = append(f.sequenceBuf[:0], prefix...)
}

// beginHold starts holding an escape string whose introducer is b, when
// KeysOnIdle applies, noting whether it began inside the quarantine.
func (f *Filter) beginHold(armed bool, introducer byte) {
	if !f.KeysOnIdle {
		return
	}
	f.holding = true
	f.heldArmed = armed
	f.held = append(f.held[:0], escapeByte, introducer)
}

func (f *Filter) resetSequenceState() {
	f.mode = filterModeIdle
	f.sequenceBuf = f.sequenceBuf[:0]
	f.escapeSeenInDiscard = false
	f.holding = false
	f.heldArmed = false
	f.held = f.held[:0]
}

func (f *Filter) discardRestOfEscapeString(lastByte byte) {
	terminated := isEscapeStringTerminated(f.sequenceBuf)
	holding, heldArmed, held := f.holding, f.heldArmed, f.held
	f.resetSequenceState()
	if !terminated {
		f.mode = filterModeDiscardEscapeString
		f.escapeSeenInDiscard = lastByte == escapeByte
		f.holding, f.heldArmed, f.held = holding, heldArmed, held
	}
}

// Consume filters a chunk of bytes. Escape-string replies (OSC/DCS/APC/PM/SOS)
// are discarded unconditionally — they have no keyboard overlap, so a human
// cannot produce them, and leaking them to the inner PTY has real-world
// failure modes (see #731: iTerm2 XTVERSION DCS leaking as `TERM2 3.6.10n`
// input into the wrapped agent), except as ForwardColorReplies allows.
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
			if out = f.hold(out, b); f.mode == filterModeIdle {
				continue // released as input past heldInputMaxLen
			}
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

			// DA/DSR replies (final bytes c/n/R) pass through so tmux can
			// negotiate modifyOtherKeys with the host terminal (see #738
			// regression from @Clean-Cole). Keyboard CSIs pass through
			// unconditionally too. Only non-whitelisted CSIs are gated by
			// armed.
			if armed && !isKeyboardCSIFinalByte(b) && !isTmuxCapabilityReplyCSIFinalByte(b) {
				f.resetSequenceState()
				continue
			}

			// #2356: while armed (the post-attach/switch quarantine window),
			// a DA/DSR reply addressed to the *previous* client's query can
			// still be in flight over the network when the next attach
			// starts. Because it is indistinguishable byte-for-byte from a
			// fresh reply, tmux's own client consumes the first one it sees
			// as the answer to its own query and has no outstanding request
			// left for any further copy — which then falls through as
			// literal keyboard input into the pane (issue #2356: stray DA1/
			// DA2 text leaking into the session prompt on session switch).
			// tmux negotiates at most one reply per kind (DA1, DA2, DSR, CPR) per
			// client attach, so budget each kind to a single pass-through
			// while armed; extra copies within the same quarantine window
			// are almost certainly stale replies from a torn-down attach and
			// are dropped rather than forwarded. Outside the quarantine
			// window (armed=false) the original unconditional passthrough
			// is preserved unchanged.
			if armed && isTmuxCapabilityReplyCSIFinalByte(b) {
				kind := classifyReply(f.sequenceBuf, b)
				if f.usedReplyKind[kind] {
					f.resetSequenceState()
					continue
				}
				f.usedReplyKind[kind] = true
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

		case filterModeCollectColorReply:
			if out = f.hold(out, b); f.mode == filterModeIdle {
				continue // released as input past heldInputMaxLen
			}
			f.sequenceBuf = append(f.sequenceBuf, b)
			if !mayBeColorReply(f.sequenceBuf) || len(f.sequenceBuf) > colorReplyMaxLen {
				f.discardRestOfEscapeString(b)
				continue
			}
			if isEscapeStringTerminated(f.sequenceBuf) {
				out = flushSequence(out, f.sequenceBuf)
				f.resetSequenceState()
			}
			continue
		}

		if f.pendingEsc {
			f.pendingEsc = false
			switch {
			case b == operatingSystemCommandByte && f.ForwardColorReplies:
				f.beginSequence(filterModeCollectColorReply, escapeByte, operatingSystemCommandByte)
				f.beginHold(armed, b)
				continue
			case isEscapeStringIntroducer(b):
				// Escape-string replies (DCS/OSC/APC/PM/SOS) are
				// stripped regardless of armed state to prevent
				// terminal-response leaks like #731. A keyboard chord
				// can start one too; KeysOnIdle holds it for FlushIdle.
				f.mode = filterModeDiscardEscapeString
				f.beginHold(armed, b)
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
		return out
	}

	// Outside the post-attach quarantine, a trailing ESC is almost always a
	// lone keyboard press: terminals write reply sequences atomically, so a
	// chunk-split exactly between ESC and its introducer is vanishingly rare
	// on a tty fd. Flushing eagerly preserves bare-ESC and ESC-ESC bindings
	// that would otherwise sit forever in pendingEsc waiting for a follow-up
	// byte that never comes. Armed = keep buffering for cross-chunk replies.
	if f.pendingEsc && !armed {
		out = append(out, escapeByte)
		f.pendingEsc = false
	}

	return out
}
