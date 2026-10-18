package session

import (
	"strings"
	"testing"
)

// The notification bar becomes the global tmux status-left, a format string:
// "#(cmd)" there runs cmd through the shell on every status redraw of every
// client on the socket. A session title (from an OpenClaw agent name, a web
// or CLI rename, a Claude session name) must reach it with "#" doubled, in
// both bar layouts.
func TestNotificationBar_EscapesTmuxFormatInTitle(t *testing.T) {
	for _, showAll := range []bool{false, true} {
		nm := NewNotificationManager(6, showAll, false)
		inst := &Instance{ID: "a", Title: "x#(touch /tmp/pwned)#[fg=red]", Status: StatusWaiting}
		nm.SyncFromInstances([]*Instance{inst}, "")
		bar := nm.FormatBar()
		if !strings.Contains(bar, "x##(touch /tmp/pwned)##[fg=red]") {
			t.Errorf("showAll=%v: escaped title missing from bar %q", showAll, bar)
		}
		if rest := strings.ReplaceAll(bar, "##", ""); strings.Contains(rest, "#(") || strings.Contains(rest, "#[") {
			t.Errorf("showAll=%v: unescaped tmux format in status-left bar: %q", showAll, bar)
		}
	}
}
