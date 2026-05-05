package costs_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func testStore(t *testing.T) *costs.Store {
	t.Helper()
	dir := t.TempDir()
	sdb, err := statedb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return costs.NewStore(sdb.DB())
}

func TestStore_WriteThenRead(t *testing.T) {
	s := testStore(t)

	ev := costs.CostEvent{
		ID:               "evt-1",
		SessionID:        "sess-1",
		Timestamp:        time.Now(),
		Model:            "claude-sonnet-4-6",
		InputTokens:      4231,
		OutputTokens:     1892,
		CacheReadTokens:  3500,
		CacheWriteTokens: 0,
		CostMicrodollars: 41193,
	}

	if err := s.WriteCostEvent(ev); err != nil {
		t.Fatalf("WriteCostEvent: %v", err)
	}

	summary, err := s.TotalBySession("sess-1")
	if err != nil {
		t.Fatalf("TotalBySession: %v", err)
	}
	if summary.TotalCostMicrodollars != 41193 {
		t.Errorf("cost = %d, want 41193", summary.TotalCostMicrodollars)
	}
	if summary.TotalInputTokens != 4231 {
		t.Errorf("input = %d, want 4231", summary.TotalInputTokens)
	}
	if summary.EventCount != 1 {
		t.Errorf("count = %d, want 1", summary.EventCount)
	}
}

func TestStore_TotalToday(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "e1", SessionID: "s1", Timestamp: now,
		Model: "claude-sonnet-4-6", InputTokens: 1000, OutputTokens: 500,
		CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "e2", SessionID: "s2", Timestamp: now,
		Model: "gemini-2.5-pro", InputTokens: 2000, OutputTokens: 1000,
		CostMicrodollars: 20000,
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := s.TotalToday()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCostMicrodollars != 30000 {
		t.Errorf("today total = %d, want 30000", summary.TotalCostMicrodollars)
	}
	if summary.EventCount != 2 {
		t.Errorf("count = %d, want 2", summary.EventCount)
	}
}

func TestStore_CostByModel(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	_ = s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "claude-sonnet-4-6", CostMicrodollars: 10000})
	_ = s.WriteCostEvent(costs.CostEvent{ID: "e2", SessionID: "s1", Timestamp: now, Model: "claude-sonnet-4-6", CostMicrodollars: 5000})
	_ = s.WriteCostEvent(costs.CostEvent{ID: "e3", SessionID: "s2", Timestamp: now, Model: "gemini-2.5-pro", CostMicrodollars: 20000})

	byModel, err := s.CostByModel()
	if err != nil {
		t.Fatal(err)
	}
	if byModel["claude-sonnet-4-6"] != 15000 {
		t.Errorf("claude = %d, want 15000", byModel["claude-sonnet-4-6"])
	}
	if byModel["gemini-2.5-pro"] != 20000 {
		t.Errorf("gemini = %d, want 20000", byModel["gemini-2.5-pro"])
	}
}

func TestStore_TopSessionsByCost(t *testing.T) {
	s := testStore(t)
	now := time.Now()

	_ = s.WriteCostEvent(costs.CostEvent{ID: "e1", SessionID: "s1", Timestamp: now, Model: "m", CostMicrodollars: 50000})
	_ = s.WriteCostEvent(costs.CostEvent{ID: "e2", SessionID: "s2", Timestamp: now, Model: "m", CostMicrodollars: 30000})
	_ = s.WriteCostEvent(costs.CostEvent{ID: "e3", SessionID: "s3", Timestamp: now, Model: "m", CostMicrodollars: 70000})

	top, err := s.TopSessionsByCost(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("len = %d, want 2", len(top))
	}
	if top[0].SessionID != "s3" {
		t.Errorf("top[0] = %s, want s3", top[0].SessionID)
	}
	if top[1].SessionID != "s1" {
		t.Errorf("top[1] = %s, want s1", top[1].SessionID)
	}
}

func TestStore_Retention(t *testing.T) {
	s := testStore(t)
	old := time.Now().AddDate(0, 0, -100)
	recent := time.Now()

	_ = s.WriteCostEvent(costs.CostEvent{ID: "old", SessionID: "s1", Timestamp: old, Model: "m", CostMicrodollars: 10000})
	_ = s.WriteCostEvent(costs.CostEvent{ID: "new", SessionID: "s1", Timestamp: recent, Model: "m", CostMicrodollars: 20000})

	deleted, err := s.PurgeOlderThan(90)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	summary, _ := s.TotalBySession("s1")
	if summary.TotalCostMicrodollars != 20000 {
		t.Errorf("remaining = %d, want 20000", summary.TotalCostMicrodollars)
	}
}

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "$0.00"},
		{1_000_000, "$1.00"},
		{12_345_678, "$12.35"},
		{500, "$0.00"},
	}
	for _, tt := range tests {
		got := costs.FormatUSD(tt.input)
		if got != tt.want {
			t.Errorf("FormatUSD(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// todayStartUTC returns 00:00:00 UTC of the current date.
func todayStartUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// monthStartUTC returns the first instant of the current month, UTC.
func monthStartUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func TestStore_TotalYesterday_NoEvents(t *testing.T) {
	s := testStore(t)
	summary, err := s.TotalYesterday()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalCostMicrodollars != 0 || summary.EventCount != 0 {
		t.Errorf("empty: cost=%d count=%d, want 0/0", summary.TotalCostMicrodollars, summary.EventCount)
	}
}

func TestStore_TotalYesterday_OnlyTodayEvent(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-today", SessionID: "s1", Timestamp: time.Now(),
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalYesterday()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("today only: yesterday total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalYesterday_OnlyYesterdayEvent(t *testing.T) {
	s := testStore(t)
	yesterdayMidday := todayStartUTC().Add(-12 * time.Hour) // yesterday 12:00 UTC
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-y1", SessionID: "s1", Timestamp: yesterdayMidday,
		Model: "claude-sonnet-4-6", CostMicrodollars: 50000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalYesterday()
	if summary.TotalCostMicrodollars != 50000 {
		t.Errorf("yesterday: total = %d, want 50000", summary.TotalCostMicrodollars)
	}
	if summary.EventCount != 1 {
		t.Errorf("yesterday: count = %d, want 1", summary.EventCount)
	}
}

func TestStore_TotalYesterday_TwoDaysAgoExcluded(t *testing.T) {
	s := testStore(t)
	twoDaysAgoMidday := todayStartUTC().Add(-36 * time.Hour) // day before yesterday 12:00 UTC
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-old", SessionID: "s1", Timestamp: twoDaysAgoMidday,
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalYesterday()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("two days ago: yesterday total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastWeek_NoEvents(t *testing.T) {
	s := testStore(t)
	summary, _ := s.TotalLastWeek()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("empty: last-week total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastWeek_OnlyThisWeekEvent(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-tw", SessionID: "s1", Timestamp: time.Now(),
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastWeek()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("this-week only: last-week total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastWeek_OnlyLastWeekEvent(t *testing.T) {
	s := testStore(t)
	// Compute the midpoint of last week deterministically: walk back to this
	// week's Monday, then go to last Thursday (Monday-7+3 = -4 days from
	// this Monday). This avoids the previous fragility where "9 days ago"
	// landed in the week-before-last when the test ran on Monday UTC.
	now := time.Now()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7 // Monday=0..Sunday=6
	thisMonday := now.AddDate(0, 0, -daysSinceMonday)
	lastWeekMidpoint := thisMonday.AddDate(0, 0, -4)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-lw", SessionID: "s1", Timestamp: lastWeekMidpoint,
		Model: "claude-sonnet-4-6", CostMicrodollars: 70000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastWeek()
	if summary.TotalCostMicrodollars != 70000 {
		t.Errorf("last-week: total = %d, want 70000", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastWeek_TwoWeeksAgoExcluded(t *testing.T) {
	s := testStore(t)
	twoWeeksAgo := time.Now().AddDate(0, 0, -16)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-2w", SessionID: "s1", Timestamp: twoWeeksAgo,
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastWeek()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("two weeks ago: last-week total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastMonth_NoEvents(t *testing.T) {
	s := testStore(t)
	summary, _ := s.TotalLastMonth()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("empty: last-month total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastMonth_OnlyThisMonthEvent(t *testing.T) {
	s := testStore(t)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-tm", SessionID: "s1", Timestamp: time.Now(),
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastMonth()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("this-month only: last-month total = %d, want 0", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastMonth_OnlyLastMonthEvent(t *testing.T) {
	s := testStore(t)
	// Mid last month: subtract 15 days from this month's start gives a date
	// firmly inside the previous calendar month for any current month.
	midLastMonth := monthStartUTC().AddDate(0, 0, -15)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-lm", SessionID: "s1", Timestamp: midLastMonth,
		Model: "claude-sonnet-4-6", CostMicrodollars: 90000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastMonth()
	if summary.TotalCostMicrodollars != 90000 {
		t.Errorf("last-month: total = %d, want 90000", summary.TotalCostMicrodollars)
	}
}

func TestStore_TotalLastMonth_TwoMonthsAgoExcluded(t *testing.T) {
	s := testStore(t)
	// Mid two months ago: 1 month before last month's mid-point.
	midTwoMonthsAgo := monthStartUTC().AddDate(0, -1, -15)
	if err := s.WriteCostEvent(costs.CostEvent{
		ID: "evt-2m", SessionID: "s1", Timestamp: midTwoMonthsAgo,
		Model: "claude-sonnet-4-6", CostMicrodollars: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.TotalLastMonth()
	if summary.TotalCostMicrodollars != 0 {
		t.Errorf("two months ago: last-month total = %d, want 0", summary.TotalCostMicrodollars)
	}
}
