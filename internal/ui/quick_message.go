package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type promptDeliveryMsg struct{ err error }

// quickMessageCmd uses the session-bound API for OpenCode 2 queueing, and
// session send's readiness, draft protection, and reporting for terminal delivery.
// Other queue requests wait for turn end rather than using durable delivery retry.
func quickMessageCmd(profile string, inst *session.Instance, msg promptSubmitMsg) tea.Cmd {
	tool := inst.Tool
	openCode2 := inst.SupportsOpenCodeQueue()
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
		// Send never interrupts: a busy Claude composer queues the message, as
		// `o` did before this composer existed (#1410, #2652). session send owns
		// the draft guard and the per-session send lock, so no keys are sent here.
		if err := quickMessageRunSend(args, msg.text); err != nil {
			return promptDeliveryMsg{err: err}
		}
		return promptDeliveryMsg{}
	}
}

// quickMessageRunSend runs `agent-deck session send` with the message on
// stdin. It is a variable so tests can observe delivery without a subprocess.
var quickMessageRunSend = func(args []string, text string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = strings.NewReader(text)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("message delivery failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// quickMessageArgs chooses immediate delivery or turn-end deferral for session send.
func quickMessageArgs(profile string, msg promptSubmitMsg) []string {
	args := []string{"--profile", profile, "session", "send", msg.instanceID, "--message-file", "-"}
	if msg.queue {
		return append(args, "--defer-if-busy")
	}
	return append(args, "--no-wait")
}
