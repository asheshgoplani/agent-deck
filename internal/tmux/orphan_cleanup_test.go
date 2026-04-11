package tmux

import "testing"

func TestIsAgentDeckControlPipe(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{
			name:    "agent-deck control pipe",
			command: "tmux -C attach-session -t agentdeck_Helios_f809d932",
			want:    true,
		},
		{
			name:    "agent-deck control pipe with absolute tmux path",
			command: "/opt/homebrew/bin/tmux -C attach-session -t agentdeck_grav-api_67b3c715",
			want:    true,
		},
		{
			name:    "non-agent-deck control pipe",
			command: "tmux -C attach-session -t my-session",
			want:    false,
		},
		{
			name:    "normal tmux attach-session",
			command: "tmux attach-session -t agentdeck_Helios_f809d932",
			want:    false, // missing -C
		},
		{
			name:    "tmux new-session with agentdeck prefix",
			command: "tmux new-session -d -s agentdeck_foo_abc",
			want:    false, // not attach-session
		},
		{
			name:    "unrelated process",
			command: "bash -c 'echo tmux -C attach-session agentdeck_'",
			want:    true, // best-effort string match — caller must also check ppid
		},
		{
			name:    "empty",
			command: "",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAgentDeckControlPipe(tc.command)
			if got != tc.want {
				t.Errorf("isAgentDeckControlPipe(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
