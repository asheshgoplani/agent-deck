package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/openclaw"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBuildOpenClawBridgeCommand(t *testing.T) {
	tests := []struct {
		name    string
		agentID string
		want    string
	}{
		{
			name:    "simple id remains unquoted",
			agentID: "agent-123",
			want:    "agent-deck openclaw bridge --agent agent-123",
		},
		{
			name:    "spaces are shell-quoted",
			agentID: "agent with spaces",
			want:    "agent-deck openclaw bridge --agent 'agent with spaces'",
		},
		{
			name:    "single quote is escaped safely",
			agentID: "agent'$(whoami)",
			want:    "agent-deck openclaw bridge --agent 'agent'\"'\"'$(whoami)'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildOpenClawBridgeCommand(tc.agentID)
			if got != tc.want {
				t.Fatalf("buildOpenClawBridgeCommand(%q) = %q, want %q", tc.agentID, got, tc.want)
			}
		})
	}
}

func TestDisplayRemote_StripsControlCharacters(t *testing.T) {
	tests := map[string]string{
		"1.2.3":              "1.2.3",
		"v1\r\nfake line":    "v1fake line",
		"\x1b[31mred\x1b[0m": "[31mred[0m",
		"a\u009b2Jb":         "a2Jb",
		"tab\there\x07\x7f":  "tabhere",
		"unicode ok \u2713":  "unicode ok \u2713",
	}
	for in, want := range tests {
		if got := displayRemote(in); got != want {
			t.Errorf("displayRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// `openclaw sync` stores this name as the session title, which `list` and the
// TUI print on every later open, so it must arrive clean.
func TestResolveAgentName_CleansGatewayText(t *testing.T) {
	osc := "\x1b]52;c;aGk=\x07"
	tests := []struct {
		name  string
		agent openclaw.AgentSummary
		want  string
	}{
		{"identity name", openclaw.AgentSummary{ID: "a1", Identity: &openclaw.AgentIdentity{Name: "Bot" + osc}}, "Bot]52;c;aGk="},
		{"plain name", openclaw.AgentSummary{ID: "a1", Name: "ops\r\n‮txt"}, "opstxt"},
		{"id fallback", openclaw.AgentSummary{ID: "a1" + osc}, "a1]52;c;aGk="},
		{"control-only name falls back", openclaw.AgentSummary{ID: "a1", Name: "\x1b\x07"}, "a1"},
	}
	for _, tc := range tests {
		if got := resolveAgentName(tc.agent); got != tc.want {
			t.Errorf("%s: resolveAgentName = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Titles are stored cleaned, so a rename to "alpha" plus a control character
// must collide with an existing "alpha" instead of creating a duplicate.
func TestCheckTitleConflict_ComparesCleanedTitle(t *testing.T) {
	alpha := &session.Instance{ID: "alpha-id", Title: "alpha", ProjectPath: "/tmp/proj"}
	beta := &session.Instance{ID: "beta-id", Title: "beta", ProjectPath: "/tmp/proj"}
	if msg, _ := checkTitleConflict([]*session.Instance{alpha, beta}, beta, "alpha\x1b\u202E\a"); msg == "" {
		t.Fatal("expected a title conflict with the existing \"alpha\"")
	}
}
