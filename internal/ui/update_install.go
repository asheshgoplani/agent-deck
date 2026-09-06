package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// In-app install of the available update.
//
// The banner used to say "run: agent-deck update" and leave the user to open
// a second terminal. internal/update already has the installer
// (PerformVerifiedUpdate), but it prints progress to stdout and the CLI
// wraps it in a changelog view, a confirmation prompt and the Homebrew
// detour. Rather than duplicate that, the install_update hotkey suspends the
// TUI (tea.Exec, exactly like attaching to tmux) and runs `<exe> update` on
// the real terminal. When it returns the binary watch re-stats the file, so
// a successful install flips the banner straight to "vX installed, press
// ctrl+t to restart" without the user reopening anything.

// updateInstallFinishedMsg is delivered when `<exe> update` exits.
type updateInstallFinishedMsg struct {
	err error
}

// installUpdateCmd is the tea.ExecCommand that runs the updater on the
// terminal the TUI just released. Stdio is wired to the process's own
// files, not Bubble Tea's CSI u input reader, so the Y/n prompt reads plain
// bytes (same reasoning as attachCmd).
type installUpdateCmd struct {
	exe string
}

func (c installUpdateCmd) Run() error {
	cmd := exec.Command(c.exe, "update")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c installUpdateCmd) SetStdin(io.Reader)  {}
func (c installUpdateCmd) SetStdout(io.Writer) {}
func (c installUpdateCmd) SetStderr(io.Writer) {}

// installUpdateKeyLabel is the key shown in the nudge banner.
func (h *Home) installUpdateKeyLabel() string {
	if key := h.actionKey(hotkeyInstallUpdate); key != "" {
		return key
	}
	return defaultHotkeyBindings[hotkeyInstallUpdate]
}

// installBlockReason returns "" when the updater may run now.
func (h *Home) installBlockReason() string {
	switch {
	case h.hasModalVisible() || h.insertMode:
		return "close the open dialog first"
	case h.installedUpdateVersion() != "":
		return fmt.Sprintf("v%s is already installed, press %s to restart", h.installedUpdateVersion(), h.restartDeckKeyLabel())
	case h.updateInfo == nil || !h.updateInfo.Available:
		return fmt.Sprintf("no update available (running v%s)", Version)
	case h.restartExecutable() == "":
		return "executable path unknown"
	}
	return ""
}

// tryInstallUpdate is the install_update key handler.
func (h *Home) tryInstallUpdate() (tea.Model, tea.Cmd) {
	if reason := h.installBlockReason(); reason != "" {
		h.setError(errors.New("install blocked: " + reason))
		return h, nil
	}
	exe := h.restartExecutable()
	uiLog.Info("tui_update_install_started", "exe", exe, "latest", h.updateInfo.LatestVersion)
	return h, tea.Exec(installUpdateCmd{exe: exe}, func(err error) tea.Msg {
		return updateInstallFinishedMsg{err: err}
	})
}

// handleUpdateInstallFinished re-checks the binary on disk right away (so
// the banner flips to "installed" on this frame rather than the next tick)
// and refreshes update info, whose cache the installer just invalidated.
func (h *Home) handleUpdateInstallFinished(msg updateInstallFinishedMsg) tea.Cmd {
	if msg.err != nil {
		h.setError(fmt.Errorf("agent-deck update failed (%v); run it in a terminal to see why", msg.err))
	}
	return tea.Batch(h.pollBinaryChange(), h.checkForUpdate())
}
