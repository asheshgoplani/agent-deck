package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ASK-92: the TUI's attach arms the image-paste hint over plain ssh, for a
// tool that reads pasted images, and leaves the rest of its options alone.
func TestWithSSHPasteHint_ArmsTheTUIAttachOnlyOverSSH(t *testing.T) {
	pane := func() string { return "" }
	notice := func(string) {}
	base := tmux.AttachOptions{DetachByte: 17, SwitchKeyByte: 7}

	t.Setenv("SSH_CONNECTION", "10.0.0.2 52144 10.0.0.9 22")
	opts := withSSHPasteHint(base, "claude", pane, notice)
	if opts.OnCtrlV == nil {
		t.Fatal("over ssh, a Claude attach reports Ctrl+V to the hint")
	}
	if opts.DetachByte != 17 || opts.SwitchKeyByte != 7 {
		t.Fatalf("the other attach options are kept: %+v", opts)
	}
	if withSSHPasteHint(base, "shell", pane, notice).OnCtrlV != nil {
		t.Fatal("a shell reads no pasted image: no hint")
	}

	t.Setenv("SSH_CONNECTION", "")
	if withSSHPasteHint(base, "claude", pane, notice).OnCtrlV != nil {
		t.Fatal("not over ssh: no hint")
	}
}
