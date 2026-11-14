package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// C9 (prime-agent adoption): with [costs.budgets] enforce = true, cost
// budgets gate agent-initiated sends and wakes. This file drives the built
// binary only. Each case is under budget passes, over budget refused before
// anything is typed, no budget configured unchanged, and limits without
// enforce stay display-only.

// budgetHome is an isolated agent-deck home with one stopped Claude session
// that has spentUSD of cost today, and config.toml holding configTOML.
func budgetHome(t *testing.T, configTOML string, spentUSD float64) (home, id string) {
	t.Helper()
	home = t.TempDir()
	id = addSessionJSON(t, home, "budget-target", "claude")
	dbPath := budgetStateDB(t, home)
	if spentUSD > 0 {
		db, err := statedb.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := costs.NewStore(db.DB()).WriteCostEvent(costs.CostEvent{
			ID: "budget-ev", SessionID: id, Timestamp: time.Now(), Model: "claude-test",
			InputTokens: 1000, CostMicrodollars: int64(spentUSD * 1e6),
		}); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
	if configTOML != "" {
		cfgDir := filepath.Join(home, ".config", "agent-deck")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, id
}

// budgetStateDB is the state.db of the profile budgetHome's sessions live in.
func budgetStateDB(t *testing.T, home string) string {
	t.Helper()
	var dbPath string
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "state.db" && strings.Contains(p, "ch_support_test") {
			dbPath = p
		}
		return nil
	})
	if dbPath == "" {
		t.Fatal("profile state.db not found")
	}
	return dbPath
}

func budgetEventWritten(t *testing.T, home string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 4<<20 {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "session.budget_exceeded") {
			found = true
		}
		return nil
	})
	return found
}

// TestSessionSendBudget drives `session send` (the path goal nudges,
// conductor heartbeats, bridge and inbox wakes all use) and `--queue`. The
// target is stopped, so a send that passes the gate fails later with "not
// running"; a refused one never gets there.
func TestSessionSendBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		spent   float64
		refused bool
	}{
		{"no budget configured", "", 5, false},
		{"under daily budget", "[costs.budgets]\nenforce = true\ndaily_limit = 100.0\n", 5, false},
		{"over daily budget", "[costs.budgets]\nenforce = true\ndaily_limit = 1.0\n", 5, true},
		{"over daily budget, enforce off (display-only)", "[costs.budgets]\ndaily_limit = 1.0\n", 5, false},
		{"cost tracking off", "[costs]\nenabled = false\n[costs.budgets]\nenforce = true\ndaily_limit = 1.0\n", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id := budgetHome(t, tc.config, tc.spent)
			for _, extra := range [][]string{nil, {"--queue"}} {
				args := append([]string{"session", "send", id, "wake up", "--json"}, extra...)
				stdout, stderr, code := runAgentDeck(t, home, args...)
				if code != 1 {
					t.Fatalf("%v: exit %d, want 1; stdout %s stderr %s", extra, code, stdout, stderr)
				}
				var res struct {
					Code     string  `json:"code"`
					Error    string  `json:"error"`
					Delivery string  `json:"delivery"`
					SendID   string  `json:"send_id"`
					LimitUSD float64 `json:"limit_usd"`
					UsedUSD  float64 `json:"used_usd"`
					ResetsAt string  `json:"resets_at"`
				}
				if err := json.Unmarshal([]byte(stdout), &res); err != nil {
					t.Fatalf("%v: decode %q: %v", extra, stdout, err)
				}
				if tc.refused {
					if res.Code != "BUDGET_EXCEEDED" || res.Delivery != "budget_exceeded" || res.SendID != "" ||
						!strings.Contains(res.Error, "budget exceeded: daily global limit exceeded ($5.00 used of $1.00)") ||
						res.LimitUSD != 1 || res.UsedUSD != 5 {
						t.Fatalf("%v: want a budget refusal before queueing, got %+v", extra, res)
					}
					if resets, err := time.Parse(time.RFC3339, res.ResetsAt); err != nil || !resets.After(time.Now()) || resets.After(time.Now().Add(25*time.Hour)) {
						t.Fatalf("%v: resets_at = %q, want the next day start", extra, res.ResetsAt)
					}
					continue
				}
				if res.Code == "BUDGET_EXCEEDED" || strings.Contains(res.Error, "budget") {
					t.Fatalf("%v: refused without a spent budget: %+v", extra, res)
				}
				if !strings.Contains(res.Error, "not running") {
					t.Fatalf("%v: want the unchanged not-running failure, got %+v", extra, res)
				}
			}
			if got := budgetEventWritten(t, home); got != tc.refused {
				t.Fatalf("session.budget_exceeded event written = %v, want %v", got, tc.refused)
			}
			if tc.refused {
				// --ignore-budget is for a person's message: it passes the
				// gate and fails later, like an unlimited send.
				for _, extra := range [][]string{nil, {"--queue"}} {
					args := append([]string{"session", "send", id, "from a person", "--json", "--ignore-budget"}, extra...)
					stdout, _, _ := runAgentDeck(t, home, args...)
					if strings.Contains(stdout, "BUDGET_EXCEEDED") || !strings.Contains(stdout, "not running") {
						t.Fatalf("%v --ignore-budget: want the not-running failure, got %s", extra, stdout)
					}
				}
				// The flag conflict is reported before the budget.
				stdout, _, code := runAgentDeck(t, home, "session", "send", id, "x", "--json", "--queue", "--wait")
				if code != 2 || !strings.Contains(stdout, "--queue is incompatible") {
					t.Fatalf("--queue --wait over budget: exit %d %s, want the flag conflict", code, stdout)
				}
			}
			// Human output names the limit and the fix.
			if tc.refused {
				_, stderr, code := runAgentDeck(t, home, "session", "send", id, "wake up", "-q")
				if code != 1 || !strings.Contains(stderr, "budget exceeded") || !strings.Contains(stderr, "[costs.budgets]") {
					t.Fatalf("human refusal: exit %d stderr %q", code, stderr)
				}
			}
		})
	}
}
