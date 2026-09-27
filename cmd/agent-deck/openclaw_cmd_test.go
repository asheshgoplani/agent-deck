package main

import "testing"

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
