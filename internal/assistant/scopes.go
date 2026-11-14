// Package assistant connects outside assistant bots (Hermes, OpenClaw, Grok
// bots, Claude, ChatGPT, Codex, Slack and Telegram bots, any MCP client or
// plain HTTP caller) to agent-deck through one scoped token.
//
// Every door ends in the same place: a token with a few scopes, checked by
// Service.Call, which rate-limits, writes one audit line and runs the
// matching agent-deck CLI command (never through a shell). The same tools
// are served as MCP over stdio (`agent-deck connect serve`), MCP over HTTP
// (POST /mcp on `agent-deck web`) and a small JSON API
// (/api/assistant/v1/...).
//
// There is deliberately no stop, remove, restart or kill tool.
package assistant

import (
	"fmt"
	"strings"
)

// Scope is one permission a token can carry.
type Scope string

const (
	// ScopeRead lists sessions and reads their status and output.
	ScopeRead Scope = "read"
	// ScopeSend types a message into a running session.
	ScopeSend Scope = "send"
	// ScopeApprove answers a visible approval prompt in a session.
	ScopeApprove Scope = "approve"
	// ScopeLaunch starts a new session.
	ScopeLaunch Scope = "launch"
)

// AllScopes is every scope in display order.
var AllScopes = []Scope{ScopeRead, ScopeSend, ScopeApprove, ScopeLaunch}

// DefaultScopes is what a token gets when nothing is asked for: read only.
var DefaultScopes = []Scope{ScopeRead}

// ScopeDescription is the one-line meaning of each scope, for help text and
// the web form.
var ScopeDescription = map[Scope]string{
	ScopeRead:    "list sessions, read their status and last output",
	ScopeSend:    "send a message to a running session",
	ScopeApprove: "answer a visible approval prompt",
	ScopeLaunch:  "start a new session",
}

// ParseScopes parses a comma-separated scope list ("read,send"). Empty input
// yields DefaultScopes. Unknown names are an error; duplicates collapse and
// the result is in AllScopes order.
func ParseScopes(s string) ([]Scope, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return append([]Scope(nil), DefaultScopes...), nil
	}
	var parts []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return NormalizeScopes(parts)
}

// NormalizeScopes validates scope names and returns them deduplicated in
// AllScopes order. An empty list yields DefaultScopes.
func NormalizeScopes(names []string) ([]Scope, error) {
	if len(names) == 0 {
		return append([]Scope(nil), DefaultScopes...), nil
	}
	seen := map[Scope]bool{}
	for _, n := range names {
		sc := Scope(strings.ToLower(strings.TrimSpace(n)))
		if _, ok := ScopeDescription[sc]; !ok {
			return nil, fmt.Errorf("unknown scope %q (valid: %s)", n, ScopeList(AllScopes))
		}
		seen[sc] = true
	}
	out := make([]Scope, 0, len(seen))
	for _, sc := range AllScopes {
		if seen[sc] {
			out = append(out, sc)
		}
	}
	return out, nil
}

// ScopeList joins scopes with commas ("read,send").
func ScopeList(scopes []Scope) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}

// HasScope reports whether scopes contains want.
func HasScope(scopes []Scope, want Scope) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// withScope returns scopes plus add, in AllScopes order.
func withScope(scopes []Scope, add Scope) []Scope {
	names := make([]string, 0, len(scopes)+1)
	for _, s := range scopes {
		names = append(names, string(s))
	}
	names = append(names, string(add))
	out, err := NormalizeScopes(names)
	if err != nil {
		return scopes
	}
	return out
}
