package ui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SWFB1018 TUI/CLI parity: a TUI native switch that restarted a Claude
// session onto another account runs the same `session switch-settle`.
func TestSwitchSettleCmd_RunsCLISettleOnlyForRestartedAccountChange(t *testing.T) {
	var called []string
	orig := switchSettleRun
	t.Cleanup(func() { switchSettleRun = orig })
	switchSettleRun = func(profile, id string, oldPIDs []int) (string, error) {
		called = append(called, fmt.Sprintf("%s/%s/%v", profile, id, oldPIDs))
		return "settle alpha: ok account=work verified", nil
	}
	h := NewHome()
	h.profile = "ops"
	restarted := &session.HarnessSwitchResult{OldTool: "claude", NewTool: "claude", OldAccount: "personal", NewAccount: "work", Restarted: true, Committed: true}
	if cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1"}); cmd != nil {
		t.Fatal("settle without a native result")
	}
	notRestarted := *restarted
	notRestarted.Restarted = false
	if cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1", nativeResult: &notRestarted}); cmd != nil {
		t.Fatal("settle for a --no-restart switch")
	}
	same := *restarted
	same.NewAccount = "personal"
	if cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1", nativeResult: &same}); cmd != nil {
		t.Fatal("settle for a no-op switch")
	}
	codex := *restarted
	codex.NewTool = "codex"
	if cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1", nativeResult: &codex}); cmd != nil {
		t.Fatal("settle for a codex session")
	}
	if cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1", nativeResult: restarted, err: errors.New("journal write failed")}); cmd != nil {
		t.Fatal("settle after a switch that reported an error (recovery required)")
	}
	cmd := h.switchSettleCmd(accountSwitchedMsg{sessionID: "s1", nativeResult: restarted, oldPIDs: []int{41, 42}})
	if cmd == nil {
		t.Fatal("no settle for a restarted account switch")
	}
	msg, ok := cmd().(switchSettledMsg)
	if !ok || len(called) != 1 || called[0] != "ops/s1/[41 42]" || msg.receipt == "" {
		t.Fatalf("msg=%#v called=%v", msg, called)
	}
	_, _ = h.Update(msg)
	if !h.confirmDialog.IsVisible() || h.confirmDialog.noticeTitle != "Account switch settled" {
		t.Fatalf("notice = %q", h.confirmDialog.noticeTitle)
	}
	h.confirmDialog.Hide()
	_, _ = h.Update(switchSettledMsg{sessionID: "s1", receipt: "settle alpha: STOPPED", err: errors.New("exit 3")})
	if h.confirmDialog.noticeTitle != "Account switch did not settle" {
		t.Fatalf("failure notice = %q", h.confirmDialog.noticeTitle)
	}
}

func TestLastSettleReceipt(t *testing.T) {
	if got := lastSettleReceipt("settle: waiting\nsettle alpha: ok account=work\n"); got != "settle alpha: ok account=work" {
		t.Fatalf("got %q", got)
	}
}
