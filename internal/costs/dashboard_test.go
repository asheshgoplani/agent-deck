package costs_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestDashboardDailyRangeBoundaries(t *testing.T) {
	s := testStore(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	from := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, ts := range []time.Time{from.Add(-time.Second), from, to.Add(-time.Second), to} {
		if err := s.WriteCostEvent(costs.CostEvent{ID: fmt.Sprint(i), SessionID: "s", Timestamp: ts, Model: "m", CostMicrodollars: int64(i+1) * 1_000_000}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DashboardDaily(1)
	if err != nil {
		t.Fatal(err)
	}
	want := []costs.DashboardDailyEntry{{Date: "2026-09-29", CostUSD: 2}, {Date: "2026-09-30", CostUSD: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("daily = %#v, want %#v", got, want)
	}
}

func TestDashboardSessionsTiesAndWindowTokens(t *testing.T) {
	s := testStore(t)
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour)
	events := []costs.CostEvent{
		{ID: "b", SessionID: "b-session", Timestamp: day, Model: "model-z", CostMicrodollars: 2_000_000, InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4},
		{ID: "az", SessionID: "a-session", Timestamp: day, Model: "model-z", CostMicrodollars: 1_000_000, InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40},
		{ID: "aa", SessionID: "a-session", Timestamp: day, Model: "model-a", CostMicrodollars: 1_000_000, InputTokens: 100, OutputTokens: 200, CacheReadTokens: 300, CacheWriteTokens: 400},
		{ID: "old", SessionID: "a-session", Timestamp: day.Add(-time.Second), Model: "model-old", CostMicrodollars: 9_000_000, InputTokens: 1000, OutputTokens: 2000, CacheReadTokens: 3000, CacheWriteTokens: 4000},
	}
	for _, event := range events {
		if err := s.WriteCostEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO instances (id,title,project_path,group_path,created_at) VALUES ('a-session','A title','/fixture','work',?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	got, err := s.DashboardSessions(500, "today")
	if err != nil {
		t.Fatal(err)
	}
	want := []costs.DashboardSessionEntry{
		{SessionID: "a-session", Title: "A title", Group: "work", CostUSD: 2, Events: 2, InputTokens: 110, OutputTokens: 220, CacheRead: 330, CacheWrite: 440, Model: "model-a"},
		{SessionID: "b-session", Title: "b-session", CostUSD: 2, Events: 1, InputTokens: 1, OutputTokens: 2, CacheRead: 3, CacheWrite: 4, Model: "model-z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("today = %#v, want %#v", got, want)
	}
	limited, err := s.DashboardSessions(1, "today")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(limited, want[:1]) {
		t.Fatalf("limited tie ordering = %#v", limited)
	}
	all, err := s.DashboardSessions(1, "all")
	if err != nil {
		t.Fatal(err)
	}
	wantAll := []costs.DashboardSessionEntry{{SessionID: "a-session", Title: "A title", Group: "work", CostUSD: 11, Events: 3, InputTokens: 1110, OutputTokens: 2220, CacheRead: 3330, CacheWrite: 4440, Model: "model-old"}}
	if !reflect.DeepEqual(all, wantAll) {
		t.Fatalf("all = %#v, want %#v", all, wantAll)
	}
}

func TestDashboardParameterBoundaries(t *testing.T) {
	s := testStore(t)
	for _, days := range []int{-1, 0, 366} {
		if _, err := s.DashboardDaily(days); err == nil {
			t.Errorf("days %d accepted", days)
		}
	}
	for _, days := range []int{1, 365} {
		if _, err := s.DashboardDaily(days); err != nil {
			t.Errorf("days %d: %v", days, err)
		}
	}
	for _, limit := range []int{-1, 0, 501} {
		if _, err := s.DashboardSessions(limit, "all"); err == nil {
			t.Errorf("limit %d accepted", limit)
		}
	}
	for _, limit := range []int{1, 500} {
		for _, period := range []string{"today", "week", "month", "all"} {
			if _, err := s.DashboardSessions(limit, period); err != nil {
				t.Errorf("limit %d period %s: %v", limit, period, err)
			}
		}
	}
	for _, period := range []string{"", "year", "ALL", "all; DROP TABLE cost_events"} {
		if _, err := s.DashboardSessions(1, period); err == nil {
			t.Errorf("period %q accepted", period)
		}
	}
}

func TestDashboardDatabaseErrors(t *testing.T) {
	queries := map[string]func(*costs.Store) error{
		"daily":    func(s *costs.Store) error { _, err := s.DashboardDaily(30); return err },
		"sessions": func(s *costs.Store) error { _, err := s.DashboardSessions(100, "all"); return err },
		"models":   func(s *costs.Store) error { _, err := s.DashboardModels("all"); return err },
		"groups":   func(s *costs.Store) error { _, err := s.DashboardGroups("all"); return err },
	}
	for _, failure := range []string{"closed", "missing table"} {
		t.Run(failure, func(t *testing.T) {
			s := testStore(t)
			if failure == "closed" {
				if err := s.DB().Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.DB().Exec(`DROP TABLE cost_events`); err != nil {
				t.Fatal(err)
			}
			for name, query := range queries {
				t.Run(name, func(t *testing.T) {
					if err := query(s); err == nil {
						t.Fatal("query succeeded with unavailable storage")
					}
				})
			}
		})
	}
}
