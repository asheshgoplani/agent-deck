package session

import (
	"os"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Image paste with the TUI on a box reached over plain ssh (ASK-92). Ctrl+V
// in the attach goes to Claude on the box, which reads the box's clipboard,
// so a screenshot copied on the owner's own machine never arrives and Claude
// answers "no image found". Only agent-deck running on that machine, with the
// box as a remote, can carry the image across (remote_image_paste.go), so the
// attach says so once.
//
// Wired into the TUI's own attach only: the box side of a remote attach is
// `agent-deck session attach`, which never shows it, and there the owner's
// agent-deck has already pasted the image or found none to paste.

// sshPasteHintNotice fits an 80-column status line: tmux cuts the rest off.
func sshPasteHintNotice() string {
	return "no image on this host: to paste one, attach from agent-deck on your machine"
}

// SSHPasteHint shows sshPasteHintNotice at most once per attach, on a lone
// Ctrl+V over the agent's composer when this box's clipboard holds no image.
type SSHPasteHint struct {
	agent     string
	readImage func() clipboardRead
	pane      func() string
	notice    func(string)

	mu       sync.Mutex
	checking bool
	shown    bool
}

// NewSSHPasteHint returns the hint for one attach of a tool's session; nil
// when this process does not run over ssh or the tool reads no pasted image.
// pane returns the session's pane as shown now, notice shows one line in the
// attach.
func NewSSHPasteHint(tool string, pane func() string, notice func(string)) *SSHPasteHint {
	return newSSHPasteHint(os.Getenv, clipboardImage, tool, pane, notice)
}

func newSSHPasteHint(getenv func(string) string, readImage func() clipboardRead, tool string, pane func() string, notice func(string)) *SSHPasteHint {
	agent := pasteAgent(tool)
	if getenv("SSH_CONNECTION") == "" || agent == "" {
		return nil
	}
	return &SSHPasteHint{agent: agent, readImage: readImage, pane: pane, notice: notice}
}

// CtrlV is called after a lone Ctrl+V was forwarded. It never waits: the
// clipboard and pane are read on their own goroutine.
func (h *SSHPasteHint) CtrlV() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.checking || h.shown {
		return
	}
	h.checking = true
	go h.check()
}

func (h *SSHPasteHint) check() {
	shown := false
	defer func() {
		h.mu.Lock()
		h.checking = false
		h.shown = h.shown || shown
		h.mu.Unlock()
	}()
	if clip := h.readImage(); len(clip.data) > 0 || clip.tooLarge || clip.timedOut {
		return // an image is (or most likely is) on this box's clipboard: Claude here reads it
	}
	if !tmux.AgentComposerVisible(h.agent, h.pane()) {
		return // an editor, a shell or a dialog: Ctrl+V was not an image paste
	}
	h.notice(sshPasteHintNotice())
	shown = true
}
