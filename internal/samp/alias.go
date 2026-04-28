package samp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir returns the SAMP message directory per SPEC.md §1:
//
//  1. $AGENT_MESSAGE_DIR if set (overrides everything)
//  2. $XDG_STATE_HOME/agent-message
//  3. $HOME/.local/state/agent-message (XDG default when XDG_STATE_HOME unset)
func DefaultDir() (string, error) {
	if d := os.Getenv("AGENT_MESSAGE_DIR"); d != "" {
		return d, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "agent-message"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("DefaultDir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "agent-message"), nil
}

// SanitizeAlias coerces an arbitrary string into a SAMP-valid alias by
// replacing every byte outside the alias character set with "-",
// trimming leading non-alphanumerics, and truncating to 64 bytes.
//
// Returns "" when the input contains no usable characters. The result
// satisfies ValidateAlias when non-empty.
func SanitizeAlias(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		valid := (r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-'
		if !valid {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = (r == '-')
	}
	out := strings.TrimLeft(b.String(), ".-_")
	if len(out) > 64 {
		out = out[:64]
	}
	out = strings.TrimRight(out, ".-_")
	if out == "" {
		return ""
	}
	if ValidateAlias(out) != nil {
		return ""
	}
	return out
}

// ResolveAlias mirrors the SAMP reference implementation's me()
// resolution: the alias an agent uses when sending from cwd.
//
// Order of precedence:
//  1. First valid line of <cwd>/.agent-message
//  2. SanitizeAlias(filepath.Base(cwd))
//
// Returns "" only when neither source yields a valid alias. Agent-deck
// uses this to derive the per-session inbox alias so unread badges line
// up with whatever name the agent's wrapper script writes under.
func ResolveAlias(cwd string) string {
	if cwd == "" {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(cwd, ".agent-message")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			s := strings.TrimSpace(line)
			if s == "" {
				continue
			}
			if a := SanitizeAlias(s); a != "" {
				return a
			}
		}
	}
	return SanitizeAlias(filepath.Base(cwd))
}
