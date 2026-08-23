package session

import "testing"

func TestSupportsNativeForkCoversEveryDispatcher(t *testing.T) {
	for _, tool := range []string{"claude", "pi", "opencode", "codex", "omp"} {
		if !SupportsNativeFork(tool) {
			t.Errorf("SupportsNativeFork(%q) = false", tool)
		}
	}
	for _, tool := range []string{"shell", "gemini", "cursor"} {
		if SupportsNativeFork(tool) {
			t.Errorf("SupportsNativeFork(%q) = true without a native dispatcher", tool)
		}
	}
}
