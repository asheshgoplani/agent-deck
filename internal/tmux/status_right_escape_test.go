package tmux

import (
	"strings"
	"testing"
)

// status-right is a tmux format string: "#(cmd)" in it runs cmd through the
// shell on every status refresh. The title (which can come from an OpenClaw
// gateway or a web request) and the project folder name (a cloned repository)
// must reach it with every "#" doubled, so tmux shows them literally.
func TestThemedStatusRight_EscapesTitleAndFolder(t *testing.T) {
	s := &Session{DisplayName: "agent#(touch /tmp/pwned)#[fg=red]", WorkDir: "/src/repo#(id)"}
	got := s.themedStatusRight(tmuxThemeStyle{hintColor: "#6a6d7c"})

	for _, want := range []string{"agent##(touch /tmp/pwned)##[fg=red]", "repo##(id)"} {
		if !strings.Contains(got, want) {
			t.Errorf("status-right %q does not contain escaped %q", got, want)
		}
	}
	// Strip the escaped "##" pairs; any "#(" left would be expanded by tmux.
	if rest := strings.ReplaceAll(got, "##", ""); strings.Contains(rest, "#(") {
		t.Fatalf("unescaped #( in status-right: %q", got)
	}
}
