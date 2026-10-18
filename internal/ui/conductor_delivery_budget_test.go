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
	gated := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "agentDeliveryOverBudget" {
					gated[fn.Name.Name] = true
				}
			}
			return true
		})
		return true
	})
	for _, fn := range []string{"sendToConductor", "backgroundStatusUpdate"} {
		if !gated[fn] {
			t.Errorf("%s types into a conductor pane without the budget gate (gated: %v)", fn, gated)
		}
	}
}
