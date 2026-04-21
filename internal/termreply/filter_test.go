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

func TestFilterDiscardsGenericCSIReplies(t *testing.T) {
	var f Filter

	got := f.Consume([]byte("\x1b[?1;2c"), true, false)
	require.Empty(t, got)
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
