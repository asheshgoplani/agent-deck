package tmux

import "testing"

// #2704: only the client with the recorded pid is picked; control clients and
// other terminals on the same server are left alone.
func TestClientNameForPID(t *testing.T) {
	line := func(ctrl, pid, name string) string {
		return ctrl + tmuxFieldSep + pid + tmuxFieldSep + name
	}
	out := line("0", "100", "/dev/pts/1") + "\n" +
		line("1", "200", "ctl") + "\n" +
		line("0", "200", "/dev/pts/2") + "\n" +
		line("0", "2000", "/dev/pts/3") + "\n"
	for pid, want := range map[int]string{100: "/dev/pts/1", 200: "/dev/pts/2", 20: "", 300: ""} {
		if got := clientNameForPID(out, pid); got != want {
			t.Errorf("clientNameForPID(%d) = %q, want %q", pid, got, want)
		}
	}
	if got := clientNameForPID("", 100); got != "" {
		t.Errorf("empty output = %q", got)
	}
}
