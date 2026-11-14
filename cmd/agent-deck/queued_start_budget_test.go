package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// `session start -m` at the group cap queues the message for a later send;
// an enforced budget that is spent must refuse it before it is queued, on the
// legacy path too (launch and the registry path already do).
func TestLegacyStartWithMessageAtCapChecksBudget(t *testing.T) {
	home, id := budgetHome(t, enforcedSpentBudget, 5)
	busy := addSessionJSON(t, home, "budget-busy", "shell")
	db, err := statedb.Open(budgetStateDB(t, home))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT OR REPLACE INTO groups (path, name, max_concurrent) VALUES ('serial', 'serial', 1)`,
		`UPDATE instances SET group_path = 'serial'`,
		`UPDATE instances SET status = 'running' WHERE id = '` + busy + `'`,
	} {
		if _, err := db.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, channelsCLIBinary(t), "session", "start", id, "-m", "do the task", "--json")
	cmd.Env = agentDeckTestEnv(home, []string{envCoreRegistry + "=0"})
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	err = cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 || !strings.Contains(outBuf.String(), `"BUDGET_EXCEEDED"`) {
		t.Fatalf("legacy session start -m at cap over budget: err %v, stdout %s", err, outBuf.String())
	}
}
