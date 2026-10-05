package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type promptDeliveryMsg struct{ err error }

type nativeQueuePane interface {
	SendKeysChunkedToPrimaryWindow(string) error
	SendNamedKeyToPrimaryWindow(string) error
}

func queueOpenCodePrompt(p nativeQueuePane, text string) error {
	if err := p.SendKeysChunkedToPrimaryWindow(text); err != nil {
		return err
	}
	for _, key := range []string{"C-x", "Enter"} {
		if err := p.SendNamedKeyToPrimaryWindow(key); err != nil {
			return fmt.Errorf("message typed but not queued: %w", err)
		}
	}
	return nil
}

// Reuse session send's readiness, draft protection, and delivery reporting for
// every harness. Queue mode uses native queueing where available, otherwise
// waits for turn end; it is not the CLI's --queue (durable delivery retry).
func quickMessageCmd(profile string, inst *session.Instance, msg promptSubmitMsg) tea.Cmd {
	tool := inst.Tool
	ts := inst.GetTmuxSession()
	busy := inst.GetStatusThreadSafe() == session.StatusRunning
	openCode2 := tool == "opencode" && inst.OpenCodeCommandName() == "opencode2"
	args := quickMessageArgs(profile, msg)
	if msg.queue && session.IsClaudeCompatible(tool) {
		// Enter is Claude's native queue action, even during generation.
		args = quickMessageArgs(profile, promptSubmitMsg{instanceID: msg.instanceID})
	}
	return func() tea.Msg {
		if msg.queue && openCode2 {
			// OpenCode 2's prompt.queue is Ctrl+X Return. Plain Return
			// steers the current turn instead, so do not substitute it.
			return promptDeliveryMsg{err: queueOpenCodePrompt(ts, msg.text)}
		}
		// Claude queues Enter during generation. Explicit steering interrupts
		// first, but never clears or merges an existing operator draft.
		if !msg.queue && busy && session.IsClaudeCompatible(tool) {
			guard := send.GuardComposerDraft(ts, conductorComposerGuardOptions())
			if guard.Refused {
				return promptDeliveryMsg{err: fmt.Errorf("message not sent: composer is occupied or unreadable; existing draft preserved")}
			}
			if err := ts.SendNamedKeyToPrimaryWindow("Escape"); err != nil {
				return promptDeliveryMsg{err: err}
			}
		}
		exe, err := os.Executable()
		if err != nil {
			return promptDeliveryMsg{err: err}
		}
		cmd := exec.Command(exe, args...)
		cmd.Stdin = strings.NewReader(msg.text)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return promptDeliveryMsg{err: fmt.Errorf("message delivery failed: %w: %s", err, strings.TrimSpace(string(output)))}
		}
		return promptDeliveryMsg{}
	}
}

func quickMessageArgs(profile string, msg promptSubmitMsg) []string {
	args := []string{"--profile", profile, "session", "send", msg.instanceID, "--message-file", "-"}
	if msg.queue {
		return append(args, "--defer-if-busy")
	}
	return append(args, "--no-wait")
}
