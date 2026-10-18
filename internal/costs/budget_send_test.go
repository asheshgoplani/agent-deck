package costs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestBudgetConfigEnforced(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  costs.BudgetConfig
		want bool
	}{
		{"none", costs.BudgetConfig{Enforce: true}, false},
		{"zero maps", costs.BudgetConfig{Enforce: true, GroupLimits: map[string]int64{"g": 0}, SessionLimits: map[string]int64{"s": -1}}, false},
		{"daily", costs.BudgetConfig{Enforce: true, DailyLimit: 1}, true},
		{"group", costs.BudgetConfig{Enforce: true, GroupLimits: map[string]int64{"g": 1}}, true},
		{"session", costs.BudgetConfig{Enforce: true, SessionLimits: map[string]int64{"s": 1}}, true},
		// Review M1: limits without enforce are display-only.
		{"daily without enforce", costs.BudgetConfig{DailyLimit: 1}, false},
		{"session without enforce", costs.BudgetConfig{SessionLimits: map[string]int64{"s": 1}}, false},
	} {
		if got := tc.cfg.Enforced(); got != tc.want {
			t.Errorf("%s: Enforced() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBudgetCheckSend(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	for i, id := range []string{"s1", "s2"} {
		if err := s.WriteCostEvent(costs.CostEvent{ID: "e" + id, SessionID: id, Timestamp: now, Model: "m", CostMicrodollars: int64(i+1) * 10_000_000}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		cfg    costs.BudgetConfig
		id     string
		group  string
		ids    []string
		want   costs.BudgetAction
		reason string
	}{
		{"no budget", costs.BudgetConfig{}, "s1", "g", []string{"s1", "s2"}, costs.BudgetActionNone, ""},
		{"under daily", costs.BudgetConfig{DailyLimit: 100_000_000}, "s1", "g", nil, costs.BudgetActionNone, ""},
		{"near daily", costs.BudgetConfig{DailyLimit: 35_000_000}, "s1", "g", nil, costs.BudgetActionWarn, "daily global limit almost reached ($30.00 used of $35.00)"},
		{"over daily", costs.BudgetConfig{DailyLimit: 30_000_000}, "s1", "g", nil, costs.BudgetActionStop, "daily global limit exceeded ($30.00 used of $30.00)"},
		{"over session", costs.BudgetConfig{SessionLimits: map[string]int64{"s2": 15_000_000}}, "s2", "g", nil, costs.BudgetActionStop, "session lifetime limit exceeded ($20.00 used of $15.00)"},
		{"other session under", costs.BudgetConfig{SessionLimits: map[string]int64{"s2": 15_000_000}}, "s1", "g", nil, costs.BudgetActionNone, ""},
		{"over group", costs.BudgetConfig{GroupLimits: map[string]int64{"g": 25_000_000}}, "s1", "g", []string{"s1", "s2"}, costs.BudgetActionStop, "group daily limit exceeded ($30.00 used of $25.00)"},
	} {
		got, err := costs.NewBudgetChecker(tc.cfg, s).CheckSend(tc.id, tc.group, tc.ids)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Action != tc.want {
			t.Errorf("%s: action = %v, want %v (%+v)", tc.name, got.Action, tc.want, got)
		}
		if tc.reason != "" && !strings.Contains(got.Describe(), tc.reason) {
			t.Errorf("%s: Describe() = %q, want %q", tc.name, got.Describe(), tc.reason)
		}
	}
}

// A refusal says when its limit's window starts over; a session lifetime
// limit never does.
func TestBudgetResultResetsAt(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: time.Now(), Model: "m", CostMicrodollars: 10_000_000}); err != nil {
		t.Fatal(err)
	}
	tz := time.UTC
	now := time.Now().In(tz)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz).AddDate(0, 0, 1)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tz).AddDate(0, 1, 0)
	for _, tc := range []struct {
		name string
		cfg  costs.BudgetConfig
		want time.Time
	}{
		{"daily", costs.BudgetConfig{DailyLimit: 1, Timezone: tz}, day},
		{"group", costs.BudgetConfig{GroupLimits: map[string]int64{"g": 1}, Timezone: tz}, day},
		{"monthly", costs.BudgetConfig{MonthlyLimit: 1, Timezone: tz}, month},
		{"session lifetime", costs.BudgetConfig{SessionLimits: map[string]int64{"s1": 1}, Timezone: tz}, time.Time{}},
	} {
		got, err := costs.NewBudgetChecker(tc.cfg, s).CheckSend("s1", "g", []string{"s1"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Action != costs.BudgetActionStop || !got.ResetsAt.Equal(tc.want) {
			t.Errorf("%s: %+v, want stop resetting at %v", tc.name, got, tc.want)
		}
	}
	weekly, _ := costs.NewBudgetChecker(costs.BudgetConfig{WeeklyLimit: 1, Timezone: tz}, s).CheckSend("s1", "g", nil)
	if d := time.Until(weekly.ResetsAt); weekly.ResetsAt.Weekday() != time.Monday || d <= 0 || d > 7*24*time.Hour {
		t.Errorf("weekly resets at %v, want the next Monday", weekly.ResetsAt)
	}
}
