package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const enforcedSpentBudget = "[costs.budgets]\nenforce = true\ndaily_limit = 1.0\n"

// An initial message is a send: `session start -m` and `launch -m` are
// refused before anything starts when an enforced budget is spent.
func TestStartAndLaunchWithMessageBudget(t *testing.T) {
	home, id := budgetHome(t, enforcedSpentBudget, 5)

	stdout, _, code := runAgentDeckBounded(t, home, "session", "start", id, "-m", "do the task", "--json")
	if code != 1 || !strings.Contains(stdout, `"BUDGET_EXCEEDED"`) {
		t.Fatalf("session start -m over budget: exit %d %s", code, stdout)
	}

	before := sessionCount(t, home)
	project := filepath.Join(home, "launch-proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, code = runAgentDeckBounded(t, home, "launch", project, "-t", "launched", "-c", "claude", "-m", "do the task", "--json")
	if code != 1 || !strings.Contains(stdout, `"BUDGET_EXCEEDED"`) {
		t.Fatalf("launch -m over budget: exit %d %s", code, stdout)
	}
	if after := sessionCount(t, home); after != before {
		t.Fatalf("launch -m over budget created a session: %d -> %d", before, after)
	}
	if !budgetEventWritten(t, home) {
		t.Fatal("no session.budget_exceeded event for the refused start/launch")
	}
}

func sessionCount(t *testing.T, home string) int {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list: exit %d %s", code, stderr)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("list --json: %v (%s)", err, stdout)
	}
	return len(rows)
}

func stubBudget(t *testing.T, r costs.BudgetResult) *int {
	t.Helper()
	calls := 0
	prev := budgetForSend
	budgetForSend = func(string, *session.Instance, []*session.Instance, *session.Storage) costs.BudgetResult {
		calls++
		return r
	}
	t.Cleanup(func() { budgetForSend = prev })
	return &calls
}

var spentResult = costs.BudgetResult{Action: costs.BudgetActionStop, Reason: "daily global limit exceeded", UsedMicro: 2e6, LimitMicro: 1e6}

// The "continue" an MCP restart types is an agent wake.
func TestSendMCPContinueBudget(t *testing.T) {
	inst := &session.Instance{ID: "mcp-target", Title: "mcp-target"}
	for _, tc := range []struct {
		name   string
		budget costs.BudgetResult
		sent   bool
	}{
		{"no budget", costs.BudgetResult{Action: costs.BudgetActionNone}, true},
		{"spent", spentResult, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubBudget(t, tc.budget)
			var typed []string
			sendMCPContinue("", nil, inst, nil, func(s string) error { typed = append(typed, s); return errors.New("ignored") }, true)
			if got := len(typed) == 1 && typed[0] == "continue"; got != tc.sent {
				t.Fatalf("continue typed = %v (%q), want %v", got, typed, tc.sent)
			}
		})
	}
}

// A send queued with --ignore-budget is a person's message: the worker
// types it even with a spent budget, without asking the gate.
func TestQueuedIgnoreBudgetSkipsTheGate(t *testing.T) {
	dir, rec := queueControlFixture(t, sendqueue.StateQueued)
	if _, err := sendqueue.Update(dir, rec.SendID, time.Now(), func(r *sendqueue.Record) { r.IgnoreBudget = true }); err != nil {
		t.Fatal(err)
	}
	stored, err := sendqueue.Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	*rec = *stored
	calls := stubBudget(t, spentResult)
	prevChild, prevRelease := sendChild, sendChildRelease
	t.Cleanup(func() { sendChild, sendChildRelease = prevChild, prevRelease })
	typed := false
	child := func(_, _, _, resultPath string) (int, func() int, error) {
		typed = true
		_ = os.WriteFile(resultPath, []byte(`{"success":true,"delivery":"delivered","submitted":true,"confirmation":"confirmed"}`), 0o600)
		return 4242, func() int { return 0 }, nil
	}
	sendChild, sendChildRelease = child, child
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err == nil {
			*rec = *r
		}
		return err
	}
	if !typeQueued("", dir, rec, &session.Instance{ID: rec.SessionID}, nil, "waiting", "", 0, set) {
		t.Fatal("typeQueued could not record the send")
	}
	if !typed || *calls != 0 {
		t.Fatalf("typed = %v, budget checks = %d; want typed without a check", typed, *calls)
	}
}

// runAgentDeckBounded is runAgentDeck with a deadline: a gate that does not
// refuse would start the agent and wait for it, so the run is killed and the
// test fails with its output instead of hanging.
func runAgentDeckBounded(t *testing.T, home string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, channelsCLIBinary(t), args...)
	cmd.Env = agentDeckTestEnv(home, nil)
	var outBuf, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%v did not finish in 60s (not refused?)\nstdout: %s\nstderr: %s", args, outBuf.String(), errBuf.String())
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run binary: %v", err)
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// Review follow-up (2): `session start -m` with the core registry off runs
// the legacy handler, which has its own gate.
func TestLegacyStartWithMessageBudget(t *testing.T) {
	home, id := budgetHome(t, enforcedSpentBudget, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, channelsCLIBinary(t), "session", "start", id, "-m", "do the task", "--json")
	cmd.Env = agentDeckTestEnv(home, []string{envCoreRegistry + "=0"})
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("legacy session start -m did not finish in 60s (not refused?): %s", outBuf.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 || !strings.Contains(outBuf.String(), `"BUDGET_EXCEEDED"`) {
		t.Fatalf("legacy session start -m over budget: err %v, stdout %s", err, outBuf.String())
	}
}
