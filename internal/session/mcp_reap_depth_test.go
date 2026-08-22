package session

import "testing"

// The reaper walks down from the pane PID and skips the tool process, because
// tmux's pgroup kill-session owns that one. Which depth the tool sits at is not
// fixed: agent-deck execs the agent where it can, so an exec'd pane is led by
// the agent itself and its MCP children are one level down, while a pane still
// led by a shell keeps the tool at depth 1 and the MCP children at depth 2.
//
// Hardcoding either depth breaks the other half. A fixed 2 leaves exec'd panes
// with nothing registered at all, so the issue #965 defence against
// setsid-escaping MCP children never arms for them; a fixed 1 would SIGTERM the
// tool in shell-led panes and trip the cosmetic teardown error.
func TestMCPReapMinDepth(t *testing.T) {
	tests := []struct {
		name           string
		paneLeaderComm string
		want           int
	}{
		{name: "agent leads the pane after exec", paneLeaderComm: "claude", want: 1},
		{name: "codex leads the pane", paneLeaderComm: "codex", want: 1},
		{name: "bash wrapper still in place", paneLeaderComm: "bash", want: 2},
		{name: "zsh wrapper still in place", paneLeaderComm: "zsh", want: 2},
		{name: "fish wrapper still in place", paneLeaderComm: "fish", want: 2},
		{name: "unreadable leader falls back to skipping depth 1", paneLeaderComm: "", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpReapMinDepth(tt.paneLeaderComm); got != tt.want {
				t.Errorf("mcpReapMinDepth(%q) = %d, want %d", tt.paneLeaderComm, got, tt.want)
			}
		})
	}
}

// comm is the last column of `ps -eo pid=,ppid=,comm=`, and the two platforms
// disagree on its shape: macOS prints an absolute path, Linux the bare name.
// Both have to reduce to something isShellBinary can match, or every macOS pane
// looks non-shell and the tool gets signalled.
func TestParsePSCommandNames(t *testing.T) {
	procTable := []byte(
		"  100     1 /bin/bash\n" +
			"  200   100 claude\n" +
			"  300   200 /usr/bin/python3.12\n" +
			"  400   300 /Applications/Foo.app/Contents/MacOS/Foo Helper\n" +
			"garbage line\n" +
			"  500   400\n",
	)

	got := parsePSCommandNames(procTable)
	want := map[int]string{
		100: "bash",
		200: "claude",
		300: "python3.12",
		// spaces survive: only the directory part is stripped
		400: "Foo Helper",
	}
	if len(got) != len(want) {
		t.Fatalf("parsePSCommandNames() = %#v, want %#v", got, want)
	}
	for pid, name := range want {
		if got[pid] != name {
			t.Errorf("pid %d = %q, want %q", pid, got[pid], name)
		}
	}

	// The same snapshot must still yield the valid parent/child relationships,
	// since both classifications are made against one ps call by design. Unlike
	// parsePSCommandNames, #1687 deliberately makes the process-tree parser
	// report malformed rows (the "garbage line" above) so an incomplete process
	// scan cannot be mistaken for an empty one.
	children, err := parsePSParentChildMap(procTable)
	if err == nil {
		t.Error("malformed ps row must be reported")
	}
	if len(children[100]) != 1 || children[100][0] != 200 {
		t.Errorf("parent/child map lost entries on 3-column output: %#v", children)
	}
}
