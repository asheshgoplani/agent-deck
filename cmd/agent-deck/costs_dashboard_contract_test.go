package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestCostsDashboardCLIContract pins the CLI contract that dashboard clients
// feature-detect: the five data subcommands exist, exit 0 with JSON in the web
// handler shapes, honor --period on sessions, and refuse with one fixed
// sentence when cost tracking is off. It only seeds through APIs that predate
// the subcommands, so on an older core it fails at runtime, not at compile.
func TestCostsDashboardCLIContract(t *testing.T) {
	bin := goldensBinary(t)
	home, env := goldensSandbox(t)
	t.Setenv("TZ", "UTC")
	env = append(env, "TZ=UTC")
	writeConfig := func(config string) {
		t.Helper()
		path := filepath.Join(home, ".config", "agent-deck", "config.toml")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("[costs.budgets]\ndaily_limit = 10\n")

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
		{ID: "contract-today", SessionID: "golden-sess-1", Timestamp: now, Model: "model-a", CostMicrodollars: 1_500_000, InputTokens: 1, OutputTokens: 2},
		{ID: "contract-old", SessionID: "golden-sess-1", Timestamp: now.AddDate(0, 0, -400), Model: "model-b", CostMicrodollars: 2_000_000, InputTokens: 10, OutputTokens: 20},
	} {
		if err := store.WriteCostEvent(e); err != nil {
			t.Fatal(err)
		}
	}

	run := func(args ...string) (string, string, int) {
		argv := append([]string{"-p", goldensProfile, "costs"}, args...)
		return runGoldensStreamsIn(t, bin, env, home, argv)
	}
	mustJSON := func(v any, args ...string) {
		t.Helper()
		out, stderr, status := run(args...)
		if status != 0 {
			t.Fatalf("costs %v: exit=%d stderr=%q stdout=%q", args, status, stderr, out)
		}
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("costs %v: stdout is not the expected JSON: %v\n%s", args, err, out)
		}
	}

	help, _, status := run("help")
	if status != 0 || !strings.Contains(help, "daily|sessions|models|groups|budgets") {
		t.Fatalf("costs help must advertise the dashboard subcommands, got exit=%d %q", status, help)
	}

	var daily []map[string]any
	mustJSON(&daily, "daily", "--json", "--days", "30")
	// Days with spend only, as the web handler reports them; the 400-day-old
	// event is outside the window.
	if len(daily) != 1 || daily[0]["date"] != now.Format("2006-01-02") || daily[0]["cost_usd"] != 1.5 {
		t.Fatalf("daily --days 30 = %v, want one row for today costing 1.5", daily)
	}

	sessionKeys := []string{"session_id", "title", "group", "cost_usd", "events", "input_tokens", "output_tokens", "cache_read", "cache_write", "model"}
	costOf := func(period string) float64 {
		t.Helper()
		var rows []map[string]any
		mustJSON(&rows, "sessions", "--json", "--period", period)
		for _, row := range rows {
			for _, key := range sessionKeys {
				if _, ok := row[key]; !ok {
					t.Fatalf("sessions row lacks %q: %v", key, row)
				}
			}
			if row["session_id"] == "golden-sess-1" {
				return row["cost_usd"].(float64)
			}
		}
		t.Fatalf("sessions --period %s has no golden-sess-1 row: %v", period, rows)
		return 0
	}
	if got := costOf("all"); math.Abs(got-3.5) > 1e-9 {
		t.Fatalf("sessions --period all cost = %v, want 3.5", got)
	}
	if got := costOf("today"); math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("sessions --period today cost = %v, want 1.5 (older events excluded)", got)
	}

	var models map[string]float64
	mustJSON(&models, "models", "--json")
	if math.Abs(models["model-a"]-1.5) > 1e-9 || math.Abs(models["model-b"]-2) > 1e-9 {
		t.Fatalf("models = %v", models)
	}

	var groups []map[string]any
	mustJSON(&groups, "groups", "--json")
	if len(groups) == 0 {
		t.Fatal("groups returned no rows")
	}
	for _, key := range []string{"group", "cost_usd", "events", "sessions"} {
		if _, ok := groups[0][key]; !ok {
			t.Fatalf("groups row lacks %q: %v", key, groups[0])
		}
	}

	var budgets map[string]map[string]float64
	mustJSON(&budgets, "budgets", "--json")
	if budgets["daily"]["limit_usd"] != 10 || math.Abs(budgets["daily"]["used_usd"]-1.5) > 1e-9 {
		t.Fatalf("budgets.daily = %v, want used 1.5 of 10", budgets["daily"])
	}

	writeConfig("[costs]\nenabled = false\n")
	for _, sub := range []string{"daily", "sessions", "models", "groups", "budgets"} {
		out, stderr, status := run(sub, "--json")
		if status == 0 || out != "" || stderr != "cost tracking is off in this profile\n" {
			t.Fatalf("costs %s with tracking off: exit=%d stdout=%q stderr=%q", sub, status, out, stderr)
		}
	}
}
