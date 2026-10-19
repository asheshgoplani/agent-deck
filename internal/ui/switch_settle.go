package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// switchSettledMsg carries the receipt of the hands-free settle the TUI runs
// after a native account switch restarted a Claude session (SWFB1018).
type switchSettledMsg struct {
	sessionID string
	receipt   string
	err       error
}

// switchSettleRun runs `agent-deck session switch-settle` for one session,
// the same code path as the CLI switch (TUI/CLI parity). It is a variable so
// tests can observe the call without a subprocess.
var switchSettleRun = func(profile, sessionID string, oldPIDs []int) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	args := []string{"session", "switch-settle", sessionID, "-q"}
	if len(oldPIDs) > 0 {
		parts := make([]string, len(oldPIDs))
		for i, pid := range oldPIDs {
			parts[i] = strconv.Itoa(pid)
		}
		args = append(args, "--old-pids", strings.Join(parts, ","))
	}
	if profile != "" {
		args = append([]string{"--profile", profile}, args...)
	}
	output, runErr := exec.CommandContext(ctx, exe, args...).CombinedOutput()
	if len(output) > 64<<10 {
		output = output[len(output)-64<<10:]
	}
	receipt := lastSettleReceipt(string(output))
	if runErr != nil {
		if receipt == "" {
			receipt = strings.TrimSpace(string(output))
		}
		return receipt, fmt.Errorf("switch-settle: %w", runErr)
	}
	return receipt, nil
}

// lastSettleReceipt picks the receipt line out of the command's output.
func lastSettleReceipt(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "settle ") {
			return lines[i]
		}
	}
	return ""
}

// switchSettleCmd returns the settle command for a committed native switch
// that restarted a Claude session onto another account, or nil.
func (h *Home) switchSettleCmd(msg accountSwitchedMsg) tea.Cmd {
	// A switch that reported an error (even one committed, the
	// recovery-required case) is left to the person reading its failure
	// notice; settle never answers prompts or kicks it, like the CLI.
	result := msg.nativeResult
	if msg.err != nil || result == nil || !result.Restarted || result.OldAccount == result.NewAccount || !session.IsClaudeCompatible(result.NewTool) {
		return nil
	}
	profile, id, oldPIDs := h.profile, msg.sessionID, msg.oldPIDs
	return func() tea.Msg {
		receipt, err := switchSettleRun(profile, id, oldPIDs)
		return switchSettledMsg{sessionID: id, receipt: receipt, err: err}
	}
}

// handleSwitchSettled shows the receipt; an unhealthy settle is a notice,
// since it names what still needs a person.
func (h *Home) handleSwitchSettled(msg switchSettledMsg) {
	if msg.err != nil {
		body := msg.receipt
		if body == "" {
			body = msg.err.Error()
		}
		h.confirmDialog.ShowNotice("Account switch did not settle", body)
		return
	}
	h.confirmDialog.ShowNotice("Account switch settled", msg.receipt)
}
