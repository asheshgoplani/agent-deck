package costs_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestBudgetDashboard_ConfiguredLimitsAndTimezone(t *testing.T) {
	s := testStore(t)
	// A non-local zone catches accidental use of Store.TotalToday's local window.
	tz := time.FixedZone("budget-zone", 13*60*60)
	now := time.Now().In(tz)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
	weekday := (int(now.Weekday()) + 6) % 7
	week := day.AddDate(0, 0, -weekday)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tz)
	events := []costs.CostEvent{
		{ID: "before-day", SessionID: "s1", Timestamp: day.Add(-time.Second), CostMicrodollars: 2_000_000},
		{ID: "at-day", SessionID: "s1", Timestamp: day, CostMicrodollars: 3_000_000},
		{ID: "at-week", SessionID: "other", Timestamp: week, CostMicrodollars: 5_000_000},
		{ID: "before-week", SessionID: "other", Timestamp: week.Add(-time.Second), CostMicrodollars: 7_000_000},
		{ID: "at-month", SessionID: "other", Timestamp: month, CostMicrodollars: 11_000_000},
		{ID: "before-month", SessionID: "s1", Timestamp: month.Add(-time.Second), CostMicrodollars: 13_000_000},
		{ID: "nested-group", SessionID: "nested", Timestamp: now, CostMicrodollars: 17_000_000},
		{ID: "deleted-session", SessionID: "deleted", Timestamp: now.AddDate(-1, 0, 0), CostMicrodollars: 19_000_000},
	}
	for _, event := range events {
		if err := s.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	for id, group := range map[string]string{"s1": "work", "other": "elsewhere", "nested": "work/child"} {
		if _, err := s.DB().Exec(`INSERT INTO instances (id,title,project_path,group_path,created_at) VALUES (?,?,?,?,?)`, id, id, "/fixture", group, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	cfg := costs.BudgetConfig{
		DailyLimit: 50_000_000, WeeklyLimit: 100_000_000, MonthlyLimit: 200_000_000, Timezone: tz,
		GroupLimits:   map[string]int64{"work": 30_000_000, "empty": 10_000_000, "disabled": 0},
		SessionLimits: map[string]int64{"s1": 40_000_000, "deleted": 50_000_000, "empty": 5_000_000, "disabled": -1},
	}
	checker := costs.NewBudgetChecker(cfg, s)
	got, err := checker.Dashboard()
	if err != nil {
		t.Fatal(err)
	}
	sum := func(since time.Time, session string) float64 {
		var total int64
		for _, event := range events {
			if !event.Timestamp.Before(since) && (session == "" || session == event.SessionID) {
				total += event.CostMicrodollars
			}
		}
		return float64(total) / 1_000_000
	}
	want := costs.BudgetDashboard{
		Daily:    &costs.BudgetUsage{UsedUSD: sum(day, ""), LimitUSD: 50},
		Weekly:   &costs.BudgetUsage{UsedUSD: sum(week, ""), LimitUSD: 100},
		Monthly:  &costs.BudgetUsage{UsedUSD: sum(month, ""), LimitUSD: 200},
		Groups:   map[string]costs.BudgetUsage{"work": {UsedUSD: sum(day, "s1"), LimitUSD: 30}, "empty": {LimitUSD: 10}},
		Sessions: map[string]costs.BudgetUsage{"s1": {UsedUSD: sum(time.Time{}, "s1"), LimitUSD: 40}, "deleted": {UsedUSD: 19, LimitUSD: 50}, "empty": {LimitUSD: 5}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dashboard = %+v, want %+v", got, want)
	}
	// Budget enforcement must see the same daily dollars at its stop threshold.
	cfg = costs.BudgetConfig{DailyLimit: 1, Timezone: tz}
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	checked, err := costs.NewBudgetChecker(cfg, s).CheckTx(tx, "s1", "work", []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Daily.UsedUSD != float64(checked.UsedMicro)/1_000_000 {
		t.Fatalf("Dashboard daily = %v, CheckTx = %v", got.Daily.UsedUSD, checked.UsedMicro)
	}
}

func TestBudgetDashboard_NoConfiguredLimits(t *testing.T) {
	s := testStore(t)
	got, err := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: -1, GroupLimits: map[string]int64{"off": 0}, SessionLimits: map[string]int64{"off": -1}}, s).Dashboard()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Fatalf("JSON = %s, want {}", data)
	}
}

func TestBudgetDashboard_QueryErrors(t *testing.T) {
	for name, cfg := range map[string]costs.BudgetConfig{
		"daily": {DailyLimit: 1}, "weekly": {WeeklyLimit: 1}, "monthly": {MonthlyLimit: 1},
		"group": {GroupLimits: map[string]int64{"work": 1}}, "session": {SessionLimits: map[string]int64{"s1": 1}},
	} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			table := "cost_events"
			if name == "group" {
				table = "instances"
			}
			if _, err := s.DB().Exec(fmt.Sprintf("DROP TABLE %s", table)); err != nil {
				t.Fatal(err)
			}
			got, err := costs.NewBudgetChecker(cfg, s).Dashboard()
			if err == nil {
				t.Fatal("Dashboard succeeded after required table was dropped")
			}
			if !reflect.DeepEqual(got, costs.BudgetDashboard{}) {
				t.Fatalf("partial report returned on failure: %+v", got)
			}
		})
	}
	t.Run("closed database", func(t *testing.T) {
		s := testStore(t)
		if err := s.DB().Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := costs.NewBudgetChecker(costs.BudgetConfig{DailyLimit: 1}, s).Dashboard(); err == nil {
			t.Fatal("Dashboard succeeded on closed database")
		}
	})
}
