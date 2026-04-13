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
