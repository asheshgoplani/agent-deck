package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

type promptDeliveryMsg struct{ err error }

// quickMessageCmd reuses session send's readiness, draft protection, and reporting for
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
			// A session-bound API request cannot drift to another tmux pane or
			// submit into a replacement shell. Never fall back to terminal keys.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return promptDeliveryMsg{err: inst.QueueOpenCodePrompt(ctx, msg.text)}
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

// quickMessageArgs chooses immediate delivery or turn-end deferral for session send.
func quickMessageArgs(profile string, msg promptSubmitMsg) []string {
	args := []string{"--profile", profile, "session", "send", msg.instanceID, "--message-file", "-"}
	if msg.queue {
		return append(args, "--defer-if-busy")
	}
	return append(args, "--no-wait")
}
