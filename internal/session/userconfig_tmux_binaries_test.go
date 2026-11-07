package session

import (
	"reflect"
	"testing"
)

// A [tmux.binaries] path whose file is not named exactly "tmux" still runs,
// but a bare "tmux" in that socket's panes (its directory goes first on
// PATH) would not find it, so config load warns about it.
func TestMisnamedTmuxBinarySockets(t *testing.T) {
	got := misnamedTmuxBinarySockets(map[string]string{
		"ok":       "/opt/tmux-3.6a/bin/tmux",
		"bare":     "tmux",
		"suffixed": "/usr/local/bin/tmux-3.6a",
		"renamed":  "/opt/bin/tmux36",
		"empty":    "  ",
	})
	want := []string{"renamed", "suffixed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("misnamedTmuxBinarySockets = %v, want %v", got, want)
	}
	if got := misnamedTmuxBinarySockets(nil); len(got) != 0 {
		t.Fatalf("nil map = %v, want none", got)
	}
}
