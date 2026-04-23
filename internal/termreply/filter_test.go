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
