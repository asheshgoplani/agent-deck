package tmux

import "testing"

func TestEmptySocketResolvesAwayFromDefault(t *testing.T) {
	for _, c := range []struct {
		name, tmuxEnv, tmpdir string
		want                  bool
	}{
		{"no tmux", "", "", false},
		{"inside default server", "/tmp/tmux-501/default,123,0", "", false},
		{"inside default server, darwin spelling", "/private/tmp/tmux-501/default,123,0", "", false},
		{"inside default server under TMUX_TMPDIR", "/x/y/tmux-501/default,1,0", "/x/y", false},
		{"inside a -L server (the rc incident)", "/private/tmp/tmux-501/uxaudit,5369,0", "", true},
		{"inside a -S server", "/tmp/ad-sock-1/s,77,0", "", true},
		{"default name under another tmpdir", "/other/tmux-501/default,1,0", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"TMUX": c.tmuxEnv, "TMUX_TMPDIR": c.tmpdir}
			if got := emptySocketResolvesAwayFromDefault(func(k string) string { return env[k] }, 501); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// A session with its own socket is never answered by the default server.
func TestAbsenceIsForeignServer_ExplicitSocketIsNeverForeign(t *testing.T) {
	s := &Session{Name: "agentdeck_x", SocketName: "work"}
	if s.AbsenceIsForeignServer() {
		t.Fatal("a session with an explicit socket must keep its own verdict")
	}
}
