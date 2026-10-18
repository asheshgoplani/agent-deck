package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func budgetTestChecker(t *testing.T, cfg costs.BudgetConfig, spent map[string]int64) *costs.BudgetChecker {
	t.Helper()
	sdb, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	store := costs.NewStore(sdb.DB())
	for id, micro := range spent {
		if err := store.WriteCostEvent(costs.CostEvent{ID: "ev-" + id, SessionID: id, Timestamp: time.Now(), Model: "m", CostMicrodollars: micro}); err != nil {
			t.Fatal(err)
		}
	}
	return costs.NewBudgetChecker(cfg, store)
}

// Review M2: deliveries the TUI types into a conductor pane itself (the
// heartbeat after a clear-on-compact, watcher health alerts) are agent
// wakes and pass the enforced budget gate.
func TestAgentDeliveryOverBudget(t *testing.T) {
	cond := &session.Instance{ID: "cond", GroupPath: "work"}
	peer := &session.Instance{ID: "peer", GroupPath: "work"}
	sub := &session.Instance{ID: "sub", GroupPath: "work/sub"}
	all := []*session.Instance{cond, peer, sub}
	for _, tc := range []struct {
		name    string
		cfg     costs.BudgetConfig
		spent   map[string]int64
		refused bool
	}{
		{"no checker", costs.BudgetConfig{}, nil, false},
		{"display-only limit", costs.BudgetConfig{DailyLimit: 1}, map[string]int64{"cond": 5}, false},
		{"enforced, under", costs.BudgetConfig{Enforce: true, DailyLimit: 100_000_000}, map[string]int64{"cond": 5}, false},
		{"enforced, spent", costs.BudgetConfig{Enforce: true, DailyLimit: 1}, map[string]int64{"cond": 5}, true},
		{"group limit spent by a peer", costs.BudgetConfig{Enforce: true, GroupLimits: map[string]int64{"work": 10}}, map[string]int64{"peer": 20}, true},
		{"group limit, spend in a subgroup", costs.BudgetConfig{Enforce: true, GroupLimits: map[string]int64{"work": 10}}, map[string]int64{"sub": 20}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Home{}
			if tc.name != "no checker" {
				h.costBudget = budgetTestChecker(t, tc.cfg, tc.spent)
			}
			reason, refused := h.agentDeliveryOverBudget(cond, all)
			if refused != tc.refused {
				t.Fatalf("refused = %v (%q), want %v", refused, reason, tc.refused)
			}
		})
	}
}

// Both TUI delivery paths ask the gate before typing.
func TestConductorDeliveriesAskTheBudgetGate(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "home.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if calls[fn.Name.Name] == nil {
						calls[fn.Name.Name] = map[string]bool{}
					}
					calls[fn.Name.Name][sel.Sel.Name] = true
				}
			}
			return true
		})
		return true
	})
	for _, fn := range []string{"sendToConductor", "scheduleClearAndHeartbeat"} {
		if !calls[fn]["agentDeliveryOverBudget"] {
			t.Errorf("%s types into a conductor pane without the budget gate", fn)
		}
	}
	if !calls["backgroundStatusUpdate"]["scheduleClearAndHeartbeat"] {
		t.Error("backgroundStatusUpdate does not send the heartbeat through scheduleClearAndHeartbeat")
	}
}

// Review follow-up (2): the result of the heartbeat gate. A spent, enforced
// budget sends nothing and leaves the conductor unmarked, so it is tried
// again; otherwise the /clear and the heartbeat are typed.
func TestScheduleClearAndHeartbeatBudgetResult(t *testing.T) {
	cond := &session.Instance{ID: "cond", Title: "conductor-x", GroupPath: "conductor"}
	for _, tc := range []struct {
		name      string
		cfg       costs.BudgetConfig
		scheduled bool
	}{
		{"under budget", costs.BudgetConfig{Enforce: true, DailyLimit: 100_000_000}, true},
		{"spent", costs.BudgetConfig{Enforce: true, DailyLimit: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typed := make(chan string, 4)
			prev := conductorHeartbeatDeliver
			conductorHeartbeatDeliver = func(_ *tmux.Session, body string) error { typed <- body; return nil }
			t.Cleanup(func() { conductorHeartbeatDeliver = prev })
			h := &Home{clearOnCompactSent: map[string]time.Time{}}
			h.costBudget = budgetTestChecker(t, tc.cfg, map[string]int64{"cond": 5})
			got := h.scheduleClearAndHeartbeat(cond, []*session.Instance{cond}, tmux.NewSession("conductor-x", t.TempDir()))
			if got != tc.scheduled {
				t.Fatalf("scheduled = %v, want %v", got, tc.scheduled)
			}
			if _, marked := h.clearOnCompactSent[cond.ID]; marked != tc.scheduled {
				t.Fatalf("marked sent = %v, want %v", marked, tc.scheduled)
			}
			select {
			case body := <-typed:
				if !tc.scheduled {
					t.Fatalf("typed %q into the pane with the budget spent", body)
				}
				if body != "/clear" {
					t.Fatalf("first delivery = %q, want /clear", body)
				}
			case <-time.After(2 * time.Second):
				if tc.scheduled {
					t.Fatal("nothing was typed for a scheduled heartbeat")
				}
			}
		})
	}
}
