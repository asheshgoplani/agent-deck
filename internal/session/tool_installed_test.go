package session

import (
	"errors"
	"testing"
)

// ASK-211: the New Session form marks missing tools with ToolInstalled, which
// probes whatever show_only_installed_tools says.
func TestToolInstalled(t *testing.T) {
	prev := lookPathFn
	t.Cleanup(func() { lookPathFn = prev })
	lookPathFn = func(name string) (string, error) {
		if name == "codex" {
			return "/usr/bin/codex", nil
		}
		return "", errors.New("not found")
	}
	cases := map[string]bool{"": true, "shell": true, "codex": true, "gemini": false}
	for name, want := range cases {
		if got := ToolInstalled(name); got != want {
			t.Errorf("ToolInstalled(%q) = %v, want %v", name, got, want)
		}
	}
}
