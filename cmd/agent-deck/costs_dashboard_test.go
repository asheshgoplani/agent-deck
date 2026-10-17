package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

const dashboardConfig = `[costs]
enabled = true
[costs.budgets]
daily_limit = 10
weekly_limit = 20
monthly_limit = 30
[costs.budgets.groups.my-sessions]
daily_limit = 5
[costs.budgets.sessions.golden-sess-1]
total_limit = 100
`

func dashboardFixture(t *testing.T) (string, []string, *costs.Store) {
	t.Helper()
	home, env := goldensSandbox(t)
	t.Setenv("TZ", "UTC")
	env = append(env, "TZ=UTC")
	dashboardWriteConfig(t, home, dashboardConfig)
	dir, err := session.GetProfileDir(goldensProfile)
	if err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := costs.NewStore(db.DB())
	now := time.Now().UTC()
	for _, e := range []costs.CostEvent{
		{ID: "today-a", SessionID: "golden-sess-1", Timestamp: now, Model: "model-a", CostMicrodollars: 1250000, InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40},
		{ID: "today-b", SessionID: "golden-sess-1", Timestamp: now, Model: "model-b", CostMicrodollars: 250000, InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4},
		{ID: "old", SessionID: "golden-sess-1", Timestamp: now.AddDate(0, 0, -400), Model: "model-b", CostMicrodollars: 2000000, InputTokens: 100, OutputTokens: 200, CacheReadTokens: 300, CacheWriteTokens: 400},
		{ID: "today-c", SessionID: "golden-sess-3", Timestamp: now, Model: "model-a", CostMicrodollars: 500000, InputTokens: 5, OutputTokens: 6, CacheReadTokens: 7, CacheWriteTokens: 8},
	} {
		if err := store.WriteCostEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	return home, env, store
}

func dashboardWriteConfig(t *testing.T, home, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-deck", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
}

func dashboardRun(t *testing.T, bin, home string, env []string, args ...string) string {
	t.Helper()
	argv := append([]string{"-p", goldensProfile, "costs"}, args...)
	out, stderr, status := runGoldensStreamsIn(t, bin, env, home, argv)
	if status != 0 {
		t.Fatalf("%v exit=%d stderr=%s", args, status, stderr)
	}
	if stderr != "" {
		t.Fatalf("%v unexpected stderr: %s", args, stderr)
	}
	return out
}

func TestCostsDashboardGoldensAndWebParity(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	srv := web.NewServer(web.Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	for _, command := range []string{"daily", "sessions", "models", "groups", "budgets"} {
		t.Run(command, func(t *testing.T) {
			out := dashboardRun(t, bin, home, env, command, "--json")
			goldenOut := out
			if command == "daily" {
				goldenOut = strings.ReplaceAll(out, time.Now().UTC().Format("2006-01-02"), "TODAY")
			}
			want, err := os.ReadFile(filepath.Join("testdata", "costs_dashboard", command+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var gotJSON, wantJSON any
			if err := json.Unmarshal([]byte(goldenOut), &gotJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantJSON); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Errorf("golden mismatch\ngot: %s\nwant: %s", goldenOut, want)
			}
			if command == "budgets" {
				return
			} // Budgets has no corresponding HTTP endpoint.
			req := httptest.NewRequest(http.MethodGet, "/api/costs/"+command, nil)
			req.Host = "localhost"
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("web status=%d: %s", rr.Code, rr.Body.String())
			}
			if out != rr.Body.String() {
				t.Errorf("CLI/web bytes differ\nCLI: %q\nweb: %q", out, rr.Body.String())
			}
		})
	}
}

func TestCostsDashboardPeriodsAndToday(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	now := time.Now().UTC()
	// Distinct historical amounts catch periods accidentally using all-time totals.
	for i, days := range []int{1, 7, 28, 45} {
		if err := store.WriteCostEvent(costs.CostEvent{ID: "period-" + time.Duration(i).String(), SessionID: "golden-sess-3", Timestamp: now.AddDate(0, 0, -days), Model: "model-a", CostMicrodollars: int64(i+1) * 1000000}); err != nil {
			t.Fatal(err)
		}
	}
	var summary map[string]int64
	if err := json.Unmarshal([]byte(dashboardRun(t, bin, home, env, "summary", "--json")), &summary); err != nil {
		t.Fatal(err)
	}
	if summary["cost_today_microdollars"] != 2000000 {
		t.Fatalf("Today includes historical spend: %v", summary)
	}
	for period, key := range map[string]string{"today": "cost_today_microdollars", "week": "cost_this_week_microdollars", "month": "cost_this_month_microdollars", "all": ""} {
		var rows []struct {
			Cost float64 `json:"cost_usd"`
		}
		out := dashboardRun(t, bin, home, env, "sessions", "--json", "--period", period)
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		srv := web.NewServer(web.Config{ListenAddr: "127.0.0.1:0"})
		srv.SetCostStore(store)
		req := httptest.NewRequest(http.MethodGet, "/api/costs/sessions?period="+period, nil)
		req.Host = "localhost"
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || out != rr.Body.String() {
			t.Errorf("period %s CLI/web differ: CLI=%q web(%d)=%q", period, out, rr.Code, rr.Body.String())
		}
		var sum float64
		for _, r := range rows {
			sum += r.Cost
		}
		want := int64(14000000)
		if key != "" {
			want = summary[key]
		}
		if int64(math.Round(sum*1000000)) != want {
			t.Errorf("period %s: %v USD, want %d microdollars", period, sum, want)
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(dashboardRun(t, bin, home, env, "sessions", "--json", "--limit", "1")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["session_id"] != "golden-sess-3" {
		t.Fatalf("limit/order: %v", rows)
	}
}

func TestCostsDashboardErrorsAndHelp(t *testing.T) {
	bin := goldensBinary(t)
	home, env, _ := dashboardFixture(t)
	for _, args := range [][]string{
		{"daily", "--json", "--days", "0"}, {"daily", "--json", "--days", "366"}, {"daily", "--json", "--days", "bad"},
		{"sessions", "--json", "--limit", "0"}, {"sessions", "--json", "--limit", "501"}, {"sessions", "--json", "--period", "year"},
		{"models", "--json", "--unknown"}, {"groups", "--json", "extra"}, {"budgets", "--json", "--limit", "1"},
	} {
		out, stderr, status := runGoldensStreamsIn(t, bin, env, home, append([]string{"-p", goldensProfile, "costs"}, args...))
		if status == 0 || out != "" || stderr == "" {
			t.Errorf("%v: exit=%d out=%q stderr=%q", args, status, out, stderr)
		}
	}
	dashboardWriteConfig(t, home, "[costs]\nenabled=false\n")
	for _, command := range []string{"daily", "sessions", "models", "groups", "budgets"} {
		out, stderr, status := runGoldensStreamsIn(t, bin, env, home, []string{"-p", goldensProfile, "costs", command, "--json"})
		if status == 0 || out != "" || !strings.Contains(stderr, "cost tracking is off in this profile") {
			t.Errorf("%s off: exit=%d stdout=%q stderr=%q", command, status, out, stderr)
		}
		help := dashboardRun(t, bin, home, env, command, "--help")
		if !strings.Contains(help, "--json") {
			t.Errorf("%s help missing JSON usage: %s", command, help)
		}
	}
	dashboardWriteConfig(t, home, "[costs]\nenabled=false\n[profiles.goldens.costs]\nenabled=true\n")
	dashboardRun(t, bin, home, env, "models", "--json")
	dashboardWriteConfig(t, home, "[costs]\nenabled=true\n[profiles.goldens.costs]\nenabled=false\n")
	_, stderr, status := runGoldensStreamsIn(t, bin, env, home, []string{"-p", goldensProfile, "costs", "models", "--json"})
	if status == 0 || !strings.Contains(stderr, "cost tracking is off in this profile") {
		t.Fatalf("profile override: exit=%d stderr=%s", status, stderr)
	}
	dashboardWriteConfig(t, home, "[costs]\nenabled=true\n")
	if got := dashboardRun(t, bin, home, env, "budgets", "--json"); strings.TrimSpace(got) != "{}" {
		t.Fatalf("unconfigured budgets: %s", got)
	}
}

func TestCostsDashboardEmptyStore(t *testing.T) {
	bin := goldensBinary(t)
	home, env := goldensSandbox(t)
	dashboardWriteConfig(t, home, "[costs]\nenabled=true\n")
	for command, want := range map[string]string{"daily": "[]", "sessions": "[]", "models": "{}", "groups": "[]", "budgets": "{}"} {
		if got := strings.TrimSpace(dashboardRun(t, bin, home, env, command, "--json")); got != want {
			t.Errorf("empty %s: got %s want %s", command, got, want)
		}
	}
}

func TestCostsDashboardQueryFailure(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	// Dropping the table is insufficient: opening storage recreates missing
	// tables. Keep it present with an unusable cost column to exercise queries.
	if _, err := store.DB().Exec(`ALTER TABLE cost_events RENAME COLUMN cost_microdollars TO broken_cost_column`); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"daily", "sessions", "models", "groups", "budgets"} {
		out, stderr, status := runGoldensStreamsIn(t, bin, env, home, []string{"-p", goldensProfile, "costs", command, "--json"})
		if status == 0 || out != "" || stderr == "" {
			t.Errorf("%s query failure: exit=%d stdout=%q stderr=%q", command, status, out, stderr)
		}
	}
}

func TestCostsDashboardSessionModelWindowAndTie(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	for _, period := range []string{"today", "all"} {
		var rows []costs.DashboardSessionEntry
		if err := json.Unmarshal([]byte(dashboardRun(t, bin, home, env, "sessions", "--json", "--period", period)), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s rows: %v", period, rows)
		}
		winner, tokens, events := "model-a", int64(11), 2
		if period == "all" {
			winner, tokens, events = "model-b", 111, 3
		}
		if rows[0].SessionID != "golden-sess-1" || rows[0].Model != winner || rows[0].InputTokens != tokens || rows[0].Events != events {
			t.Errorf("%s model/tokens/events outside selected window: %+v", period, rows[0])
		}
	}
	// model-a and model-b now tie at $2.25 each across all time. The winner
	// must be deterministic so independent CLI and HTTP refreshes cannot drift.
	if err := store.WriteCostEvent(costs.CostEvent{ID: "model-tie", SessionID: "golden-sess-1", Timestamp: time.Now().UTC(), Model: "model-a", CostMicrodollars: 1000000}); err != nil {
		t.Fatal(err)
	}
	var rows []costs.DashboardSessionEntry
	if err := json.Unmarshal([]byte(dashboardRun(t, bin, home, env, "sessions", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Model != "model-a" {
		t.Fatalf("model tie: %+v", rows)
	}
}

func TestCostsDashboardRollingPeriodGoldensAndWebParity(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	if _, err := store.DB().Exec(`DELETE FROM cost_events`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, age := range []time.Duration{time.Hour, 3 * 24 * time.Hour, 10 * 24 * time.Hour, 40 * 24 * time.Hour} {
		name := []string{"one", "two", "four", "eight"}[i]
		if err := store.WriteCostEvent(costs.CostEvent{ID: name, SessionID: "golden-sess-1", Timestamp: now.Add(-age), Model: name, CostMicrodollars: int64(1<<i) * 1000000, InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40}); err != nil {
			t.Fatal(err)
		}
	}
	srv := web.NewServer(web.Config{ListenAddr: "127.0.0.1:0"})
	srv.SetCostStore(store)
	for _, command := range []string{"sessions", "models", "groups"} {
		for _, period := range []string{"today", "7d", "30d", "all"} {
			t.Run(command+"/"+period, func(t *testing.T) {
				out := dashboardRun(t, bin, home, env, command, "--json", "--period", period)
				want, err := os.ReadFile(filepath.Join("testdata", "costs_dashboard", "rolling", command+"-"+period+".json"))
				if err != nil {
					t.Fatal(err)
				}
				// The one-hour-old row belongs to yesterday during the first UTC hour.
				if period == "today" && now.Hour() == 0 {
					want = []byte("[]\n")
					if command == "models" {
						want = []byte("{}\n")
					}
				}
				if out != string(want) {
					t.Errorf("golden mismatch: got %s want %s", out, want)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/costs/"+command+"?period="+period, nil)
				req.Host = "localhost"
				rr := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rr, req)
				if rr.Code != http.StatusOK || rr.Body.String() != out {
					t.Errorf("CLI/web bytes differ: CLI=%q web(%d)=%q", out, rr.Code, rr.Body.String())
				}
			})
		}
		out, stderr, status := runGoldensStreamsIn(t, bin, env, home, []string{"-p", goldensProfile, "costs", command, "--json", "--period", "year"})
		if status != 1 || out != "" || stderr != "period must be today, week, month, or all\n" {
			t.Errorf("%s invalid period: exit=%d out=%q stderr=%q", command, status, out, stderr)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/costs/"+command+"?period=year", nil)
		req.Host = "localhost"
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s invalid web period: %d", command, rr.Code)
		}
		help := dashboardRun(t, bin, home, env, command, "--help")
		if !strings.Contains(help, "--period today|7d|30d|all") {
			t.Errorf("%s period help missing: %s", command, help)
		}
	}
}

func TestCostsDashboardRollingCutoff(t *testing.T) {
	bin := goldensBinary(t)
	home, env, store := dashboardFixture(t)
	for _, period := range []string{"7d", "30d"} {
		if _, err := store.DB().Exec(`DELETE FROM cost_events`); err != nil {
			t.Fatal(err)
		}
		days := "-7 days"
		if period == "30d" {
			days = "-30 days"
		}
		// RFC3339 rows just outside and inside the cutoff on the same date
		// catch the lexical T-versus-space comparison bug.
		for _, shift := range []string{"-1 hour", "+1 hour"} {
			_, err := store.DB().Exec(`INSERT INTO cost_events (id, session_id, timestamp, model, cost_microdollars) VALUES (?, 'golden-sess-1', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?, ?), 'boundary', 1000000)`, shift, days, shift)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, command := range []string{"sessions", "models", "groups"} {
			out := dashboardRun(t, bin, home, env, command, "--json", "--period", period)
			if command == "models" {
				if out != "{\"boundary\":1}\n" {
					t.Errorf("%s %s: %s", command, period, out)
				}
			} else if !strings.Contains(out, `"cost_usd":1,`) || !strings.Contains(out, `"events":1,`) {
				t.Errorf("%s %s: %s", command, period, out)
			}
		}
	}
}
