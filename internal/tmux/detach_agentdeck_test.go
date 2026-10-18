package tmux

import (
	"reflect"
	"testing"
)

// #2704: only clients showing an agent-deck session are detached; control
// clients and terminals on other sessions stay attached.
func TestAgentDeckClientNames(t *testing.T) {
	line := func(ctrl, sess, name string) string {
		return ctrl + tmuxFieldSep + sess + tmuxFieldSep + name
	}
	out := line("0", SessionPrefix+"work_ab12", "/dev/pts/1") + "\n" +
		line("0", "main", "/dev/pts/2") + "\n" +
		line("1", SessionPrefix+"work_ab12", "ctl") + "\n" +
		line("0", SessionPrefix+"x_1", "") + "\n" +
		line("0", SessionPrefix+"other_cd34", "/dev/pts/3") + "\n"
	got := agentDeckClientNames(out)
	want := []string{"/dev/pts/1", "/dev/pts/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentDeckClientNames = %q, want %q", got, want)
	}
	if got := agentDeckClientNames(""); len(got) != 0 {
		t.Fatalf("empty output = %q", got)
	}
}
