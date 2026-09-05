package session

import (
	"strings"
	"testing"
)

func TestSSHAttachPortableTERM(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"xterm-256color", "xterm-256color"}, {"screen", "screen"}, {"tmux-256color", "tmux-256color"}, {"xterm-ghostty", "xterm-256color"}, {"", "xterm-256color"}, {"x; touch /unwanted", "xterm-256color"}} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("TERM", tc.input)
			r := &SSHRunner{Host: "fixture"}
			args := r.buildAttachArgs("quoted ' session")
			if command := args[len(args)-1]; !strings.HasPrefix(command, "TERM="+shellQuote(tc.want)+" ") {
				t.Fatalf("remote command: %q", command)
			}
		})
	}
}

func BenchmarkSSHAttachArgs(b *testing.B) {
	b.Setenv("TERM", "xterm-256color")
	runner := &SSHRunner{Host: "fixture"}
	for i := 0; i < b.N; i++ {
		runner.buildAttachArgs("session")
	}
}
