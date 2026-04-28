package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// buildSessionInfoForCopy produces a plain-text payload of the right-pane
// preview values (#791): Repo / Path / Branch for worktree sessions, the
// full project-path list for multi-repo sessions, or just Path for plain
// sessions. The shape is deliberately label-prefixed and newline-separated
// so a paste lands cleanly in a shell prompt, an issue tracker, or a doc
// without further editing.
func buildSessionInfoForCopy(inst *session.Instance) string {
	if inst == nil {
		return ""
	}

	var b strings.Builder

	if inst.IsMultiRepo() {
		b.WriteString("Paths:\n")
		for i, p := range inst.AllProjectPaths() {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, p)
		}
	} else if inst.IsWorktree() {
		if inst.WorktreeRepoRoot != "" {
			fmt.Fprintf(&b, "Repo: %s\n", inst.WorktreeRepoRoot)
		}
		path := inst.WorktreePath
		if path == "" {
			path = inst.ProjectPath
		}
		if path != "" {
			fmt.Fprintf(&b, "Path: %s\n", path)
		}
		if inst.WorktreeBranch != "" {
			fmt.Fprintf(&b, "Branch: %s\n", inst.WorktreeBranch)
		}
	} else if inst.ProjectPath != "" {
		fmt.Fprintf(&b, "Path: %s\n", inst.ProjectPath)
	}

	return strings.TrimRight(b.String(), "\n")
}

// copySessionInfo returns a tea.Cmd that copies the preview pane's
// session-info payload (#791) to the system clipboard, mirroring the
// fallback chain used by copySessionOutput.
func (h *Home) copySessionInfo(inst *session.Instance) tea.Cmd {
	return func() tea.Msg {
		payload := buildSessionInfoForCopy(inst)
		if payload == "" {
			return copyResultMsg{err: fmt.Errorf("no session info to copy")}
		}

		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(payload, termInfo.SupportsOSC52)
		if err != nil {
			return copyResultMsg{err: fmt.Errorf("clipboard: %w", err)}
		}
		return copyResultMsg{
			sessionTitle: inst.Title,
			lineCount:    result.LineCount,
		}
	}
}
