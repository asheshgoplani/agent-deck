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

	csiFinalArrowUpByte    = 'A'
	csiFinalArrowDownByte  = 'B'
	csiFinalArrowRightByte = 'C'
	csiFinalArrowLeftByte  = 'D'
	csiFinalEndByte        = 'F'
	csiFinalHomeByte       = 'H'
	csiFinalBacktabByte    = 'Z'
	csiFinalTildeByte      = '~'
	csiFinalKittyKeyByte   = 'u'
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
		csiFinalKittyKeyByte:
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

// Consume filters a chunk of bytes. When armed is true, terminal-generated
// control replies are discarded. If a reply started in a previous chunk, it
// continues to be discarded until it terminates even if armed is now false.
//
// Terminal replies covered here:
//   - escape-string families: OSC, DCS, APC, PM, SOS
//   - CSI replies during the quarantine window, except for a small whitelist of
//     keyboard-related CSI finals (arrows/home/end/backtab/~ keys/kitty CSI u)
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

			if armed && !isKeyboardCSIFinalByte(b) {
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
			case armed && isEscapeStringIntroducer(b):
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
