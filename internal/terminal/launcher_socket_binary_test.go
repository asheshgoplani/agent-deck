package terminal

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A socket with its own [tmux.binaries] entry is attached with that binary,
// like every other tmux call on the socket: a tmux client only talks to a
// server of its own version, so the bare "tmux" on PATH cannot attach to a
// newer server. The embedded terminal and the new-window launchers both go
// through BuildAttachCommand.
func TestBuildAttachCommand_UsesSocketBinary(t *testing.T) {
	tmux.SetSocketBinaries(map[string]string{"next": "/opt/tmux 3.6a/bin/tmux"})
	t.Cleanup(func() { tmux.SetSocketBinaries(nil) })

	got := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "next", ForceUTF8: true})
	want := "'/opt/tmux 3.6a/bin/tmux' -u -L 'next' attach -t 'myproj'"
	if got != want {
		t.Fatalf("configured socket:\n got=%q\nwant=%q", got, want)
	}

	// A socket without an entry, and the default server, keep the bare tmux.
	if got := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"}); got != "tmux -L 'agentdeck' attach -t 'myproj'" {
		t.Fatalf("unconfigured socket = %q", got)
	}
	if got := BuildAttachCommand(AttachRequest{Name: "myproj"}); got != "tmux attach -t 'myproj'" {
		t.Fatalf("default server = %q", got)
	}
}
