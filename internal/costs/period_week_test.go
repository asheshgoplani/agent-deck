package costs

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestCostPeriodWhere_WeekStartsOnCurrentMonday evaluates the "week" period's
// boundary expression for every weekday and checks it is the Monday of that
// week. On Monday the old `date('now', 'weekday 1', '-7 days')` returned the
// previous Monday, so the dashboard's "week" covered 8 days.
func TestCostPeriodWhere_WeekStartsOnCurrentMonday(t *testing.T) {
	where, err := costPeriodWhere("week")
	if err != nil {
		t.Fatal(err)
	}
	expr := string(where)
	expr = expr[strings.Index(expr, "date("):]
	if !strings.Contains(expr, "'now'") {
		t.Fatalf("week clause %q has no 'now' to substitute", where)
	}
	expr = strings.Replace(expr, "'now'", "?", 1)

	sdb, err := statedb.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })

	monday := time.Date(2025, 11, 10, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		now := monday.AddDate(0, 0, i).Add(15 * time.Hour)
		var got string
		if err := sdb.DB().QueryRow(`SELECT `+expr, now.Format("2006-01-02 15:04:05")).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "2025-11-10" {
			t.Errorf("%s: week starts %s, want 2025-11-10", now.Weekday(), got)
		}
	}
}
