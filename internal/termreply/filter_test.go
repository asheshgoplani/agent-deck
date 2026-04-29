package termreply

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterDiscardsStringRepliesAcrossChunks(t *testing.T) {
	var f Filter

	got := f.Consume([]byte("\x1b]11;rgb:d3d3/f5f5/f5f5"), true, false)
	require.Empty(t, got)
	require.True(t, f.Active())

	got = f.Consume([]byte("\x07j"), true, false)
	require.Equal(t, []byte("j"), got)
	require.False(t, f.Active())
}

// TestFilterPassesDAReplyThrough: renamed from TestFilterDiscardsGenericCSIReplies.
// The old behavior swallowed DA replies (final byte `c`), which broke tmux's
// modifyOtherKeys negotiation in iTerm2 (#738). The new contract is: DA/DSR
// replies always pass through to tmux; only outer-TUI-specific replies
// (DCS/OSC/APC/PM/SOS) are unconditionally stripped.
func TestFilterPassesDAReplyThrough(t *testing.T) {
	var f Filter

	input := []byte("\x1b[?1;2c")
	require.Equal(t, input, f.Consume(input, true, false))
	require.False(t, f.Active())
}

func TestFilterPreservesKeyboardCSIAndSS3Input(t *testing.T) {
	var f Filter

	require.Equal(t, []byte("\x1b[A"), f.Consume([]byte("\x1b[A"), true, false))
	require.False(t, f.Active())

	require.Equal(t, []byte("\x1bOA"), f.Consume([]byte("\x1bOA"), true, false))
	require.False(t, f.Active())
}

// TestFilterPreservesMouseCSIInput verifies that mouse CSI sequences
// ending in 'M' or 'm' pass through unchanged when armed. Without this,
// mouse events are silently dropped during the attach quarantine window,
// making the main-menu TUI feel frozen after detach.
func TestFilterPreservesMouseCSIInput(t *testing.T) {
	t.Run("legacy_mouse_press", func(t *testing.T) {
		var f Filter
		// ESC [ M <button> <x> <y>  (X10/legacy format, 3 bytes after 'M')
		input := []byte{0x1b, '[', 'M', ' ', '!', '"'}
		require.Equal(t, input, f.Consume(input, true, false))
	})

	t.Run("sgr_mouse_press", func(t *testing.T) {
		var f Filter
		// ESC [ < 0 ; 10 ; 20 M
		input := []byte("\x1b[<0;10;20M")
		require.Equal(t, input, f.Consume(input, true, false))
	})

	t.Run("sgr_mouse_release", func(t *testing.T) {
		var f Filter
		// ESC [ < 0 ; 10 ; 20 m
		input := []byte("\x1b[<0;10;20m")
		require.Equal(t, input, f.Consume(input, true, false))
	})
}

// Regression for #731: iTerm2 XTVERSION DCS replies can arrive on stdin long
// after the 2-second attach quarantine elapses (e.g. on window focus/resize).
// Escape-string replies (DCS/OSC/APC/PM/SOS) have no keyboard overlap and must
// be stripped regardless of the armed flag.
func TestFilterDiscardsXTVERSIONReplyWhenNotArmed(t *testing.T) {
	var f Filter

	got := f.Consume([]byte("\x1bP>|iTerm2 3.6.10n\x1b\\j"), false, false)
	require.Equal(t, []byte("j"), got)
	require.False(t, f.Active())
}

func TestFilterDiscardsOSCReplyWhenNotArmed(t *testing.T) {
	var f Filter

	got := f.Consume([]byte("\x1b]11;rgb:d3d3/f5f5/f5f5\x07k"), false, false)
	require.Equal(t, []byte("k"), got)
	require.False(t, f.Active())
}

func TestFilterDiscardsSplitDCSReplyWhenNotArmed(t *testing.T) {
	var f Filter

	got := f.Consume([]byte("\x1bP>|iTerm2 "), false, false)
	require.Empty(t, got)
	require.True(t, f.Active())

	got = f.Consume([]byte("3.6.10n\x1b\\rest"), false, false)
	require.Equal(t, []byte("rest"), got)
	require.False(t, f.Active())
}

// Regression for #738: Coleman (@Clean-Cole) reported that Shift+Enter collapsed
// to bare CR inside attached Claude/Copilot sessions because the filter was
// swallowing iTerm2's DA1 reply (`\x1b[?62;4c`). Without DA1 reaching tmux,
// tmux cannot negotiate modifyOtherKeys with the host terminal. CSI replies
// ending in `c` (DA/DA2), `n` (DSR), and `R` (cursor position) must pass
// through to tmux even during the attach quarantine window.
func TestFilterPassesDAReplyThroughEvenDuringQuarantine(t *testing.T) {
	var f Filter

	input := []byte("\x1b[?62;4c")
	require.Equal(t, input, f.Consume(input, true, false))
	require.False(t, f.Active())
}

func TestFilterPassesDA2ReplyThroughEvenDuringQuarantine(t *testing.T) {
	var f Filter

	input := []byte("\x1b[>0;95;0c")
	require.Equal(t, input, f.Consume(input, true, false))
	require.False(t, f.Active())
}

func TestFilterPassesDSRCursorReplyThroughEvenDuringQuarantine(t *testing.T) {
	var f Filter

	input := []byte("\x1b[12;34R")
	require.Equal(t, input, f.Consume(input, true, false))
	require.False(t, f.Active())
}

// Locks in that generic non-whitelisted CSI finals (e.g. arrow-like bytes
// arriving as terminal replies, or other telemetry) continue to be discarded
// while armed. The DA/DSR whitelist is a narrow carve-out, not a blanket
// passthrough. Note: arrow finals (A/B/C/D) are already whitelisted as
// keyboard input — this test uses a non-keyboard, non-reply CSI final to
// exercise the discard path.
func TestFilterDiscardsNonWhitelistedCSIWhenArmed(t *testing.T) {
	var f Filter

	// CSI ... J (ED, erase in display) is not a reply we want to pass and not
	// keyboard input. It should still be discarded during quarantine.
	got := f.Consume([]byte("\x1b[2J"), true, false)
	require.Empty(t, got)
	require.False(t, f.Active())
}

// TestRegression744_FilterPassesShiftLetterCSIUWhileArmed guards #744.
// @javierciccarelli reported that Shift+letter produced a lowercase letter
// in a remote tmux-split pane on Ghostty/SSH after the v1.7.68 termreply
// changes. Ghostty (and the kitty keyboard protocol more broadly) encodes
// Shift+A as a CSI u sequence: `\x1b[<code>;<mod>u`. Common encodings
// observed on Ghostty:
//
//   - `\x1b[65;2u`  — xterm-style: code=65 ('A'), modifier=2 (Shift)
//   - `\x1b[97;2u`  — kitty-style: code=97 ('a' unshifted), modifier=2 (Shift)
//
// Both MUST pass through Filter.Consume unchanged when the filter is armed,
// because final byte 'u' is whitelisted in isKeyboardCSIFinalByte as a
// keyboard CSI (not a terminal reply). If the filter eats either
// encoding, the PTY sees only the lowercase base byte — which is
// exactly the "Shift makes lowercase" symptom from the bug.
//
// The test also covers the split-across-chunks case: real stdin coalesces
// unpredictably, so the same CSI u split between two Consume calls must
// still round-trip byte-for-byte.
func TestRegression744_FilterPassesShiftLetterCSIUWhileArmed(t *testing.T) {
	cases := []struct {
		name string
		seq  []byte
	}{
		{"xterm-style Shift+A (\\x1b[65;2u)", []byte("\x1b[65;2u")},
		{"kitty-style Shift+A (\\x1b[97;2u)", []byte("\x1b[97;2u")},
		{"xterm-style Shift+Z (\\x1b[90;2u)", []byte("\x1b[90;2u")},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/single-chunk", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, true, false)
			require.Equal(t, tc.seq, got, "armed filter must pass Shift+letter CSI u unchanged")
			require.False(t, f.Active(), "filter must not carry parser state after a complete sequence")
		})
		t.Run(tc.name+"/split-across-chunks", func(t *testing.T) {
			var f Filter
			split := len(tc.seq) / 2
			first := f.Consume(tc.seq[:split], true, false)
			require.Empty(t, first, "mid-sequence bytes must not emit")
			require.True(t, f.Active(), "filter must keep parser state mid-sequence")
			rest := f.Consume(tc.seq[split:], true, false)
			require.Equal(t, tc.seq, append(first, rest...), "split CSI u must round-trip byte-for-byte")
		})
	}
}

// A lone ESC keypress (and ESC-ESC for jump-to-prev-message) must reach the
// inner agent outside the quarantine. Previously pendingEsc held ESC forever
// waiting for a follow-up byte that never comes for keyboard ESC.
func TestFilterFlushesLoneEscWhenNotArmed(t *testing.T) {
	t.Run("single_esc_passes_through", func(t *testing.T) {
		var f Filter
		got := f.Consume([]byte{0x1b}, false, false)
		require.Equal(t, []byte{0x1b}, got)
		require.False(t, f.Active())
	})

	t.Run("double_esc_passes_through", func(t *testing.T) {
		var f Filter
		got := f.Consume([]byte{0x1b, 0x1b}, false, false)
		require.Equal(t, []byte{0x1b, 0x1b}, got)
		require.False(t, f.Active())
	})

	t.Run("triple_esc_passes_through", func(t *testing.T) {
		var f Filter
		got := f.Consume([]byte{0x1b, 0x1b, 0x1b}, false, false)
		require.Equal(t, []byte{0x1b, 0x1b, 0x1b}, got)
		require.False(t, f.Active())
	})

	// Without the flush, a held ESC concatenates with the next read's
	// arrow into ESC ESC[A — interpreted as Alt/Meta+Up by most TUI
	// parsers ("up arrow resets the dialog" symptom).
	t.Run("lone_esc_then_arrow_in_next_chunk", func(t *testing.T) {
		var f Filter
		first := f.Consume([]byte{0x1b}, false, false)
		require.Equal(t, []byte{0x1b}, first)
		require.False(t, f.Active())

		next := f.Consume([]byte("\x1b[A"), false, false)
		require.Equal(t, []byte("\x1b[A"), next)
		require.False(t, f.Active())
	})
}

// Pins the quarantine carve-out: while armed, trailing ESC is still buffered
// across chunks so DCS/OSC reply parsing keeps working.
func TestFilterStillBuffersTrailingEscWhenArmed(t *testing.T) {
	var f Filter
	got := f.Consume([]byte{0x1b}, true, false)
	require.Empty(t, got)
	require.True(t, f.Active())

	got = f.Consume([]byte("]11;rgb:d3d3/f5f5/f5f5\x07j"), true, false)
	require.Equal(t, []byte("j"), got)
	require.False(t, f.Active())
}

// Alt+letter (iTerm2 "Esc+" Option, vim Meta bindings) hits the default
// pendingEsc branch which emits ESC+byte unchanged.
func TestFilterPassesAltLetterThrough(t *testing.T) {
	cases := []struct {
		name string
		seq  []byte
	}{
		{"alt_a", []byte{0x1b, 'a'}},
		{"alt_z", []byte{0x1b, 'z'}},
		{"alt_period", []byte{0x1b, '.'}},
		{"alt_digit", []byte{0x1b, '7'}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/not_armed", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, false, false)
			require.Equal(t, tc.seq, got)
			require.False(t, f.Active())
		})
		t.Run(tc.name+"/armed", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, true, false)
			require.Equal(t, tc.seq, got)
			require.False(t, f.Active())
		})
	}
}

// F1-F4 use SS3 (ESC O P/Q/R/S); F5-F12 use CSI ~ (ESC [ 15 ~ etc.). SS3 has
// no final-byte gating; tilde is in isKeyboardCSIFinalByte. Both pass armed.
func TestFilterPassesFunctionKeysThrough(t *testing.T) {
	cases := []struct {
		name string
		seq  []byte
	}{
		{"f1_ss3", []byte("\x1bOP")},
		{"f2_ss3", []byte("\x1bOQ")},
		{"f3_ss3", []byte("\x1bOR")},
		{"f4_ss3", []byte("\x1bOS")},
		{"f5_csi_tilde", []byte("\x1b[15~")},
		{"f6_csi_tilde", []byte("\x1b[17~")},
		{"f10_csi_tilde", []byte("\x1b[21~")},
		{"f12_csi_tilde", []byte("\x1b[24~")},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/not_armed", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, false, false)
			require.Equal(t, tc.seq, got)
			require.False(t, f.Active())
		})
		t.Run(tc.name+"/armed", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, true, false)
			require.Equal(t, tc.seq, got)
			require.False(t, f.Active())
		})
	}
}

// Focus-in (CSI I) and focus-out (CSI O) are non-keyboard, non-DA/DSR CSI:
// dropped while armed, passed through unarmed. Pins both branches so a
// future change to the gate doesn't silently swap them.
func TestFilterFocusEventBehavior(t *testing.T) {
	cases := []struct {
		name string
		seq  []byte
	}{
		{"focus_in", []byte("\x1b[I")},
		{"focus_out", []byte("\x1b[O")},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/not_armed_passes", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, false, false)
			require.Equal(t, tc.seq, got)
			require.False(t, f.Active())
		})
		t.Run(tc.name+"/armed_dropped", func(t *testing.T) {
			var f Filter
			got := f.Consume(tc.seq, true, false)
			require.Empty(t, got)
			require.False(t, f.Active())
		})
	}
}

// Known tradeoff: when armed=false and an OSC/DCS reply fragments exactly
// between ESC and its introducer, the eager flush emits ESC and the body
// arrives next read as literal text. Vanishingly rare on a local tty fd
// (terminals write replies atomically); theoretically possible over SSH.
// Pre-fix behavior buffered every keyboard ESC indefinitely — strictly
// worse for the >99% case. A future timeout-based Flush() would close
// this window; update this test to assert suppression then.
func TestFilterCrossChunkSplitAfterEscWhenNotArmed_KnownLimitation(t *testing.T) {
	var f Filter

	got := f.Consume([]byte{'x', 0x1b}, false, false)
	require.Equal(t, []byte{'x', 0x1b}, got)
	require.False(t, f.Active())

	got = f.Consume([]byte("]11;rgb:d3d3/f5f5/f5f5\x07after"), false, false)
	require.Equal(t, []byte("]11;rgb:d3d3/f5f5/f5f5\x07after"), got)
}

// Symmetric: the same split while armed stays fully suppressed end-to-end.
func TestFilterCrossChunkSplitAfterEscWhenArmed_StillSuppressed(t *testing.T) {
	var f Filter

	got := f.Consume([]byte{'x', 0x1b}, true, false)
	require.Equal(t, []byte{'x'}, got)
	require.True(t, f.Active())

	got = f.Consume([]byte("]11;rgb:d3d3/f5f5/f5f5\x07after"), true, false)
	require.Equal(t, []byte("after"), got)
	require.False(t, f.Active())
}
