package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const costsUsage = "Usage: agent-deck costs <sync|summary|export|recompute>"

func handleCosts(profile string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, costsUsage)
		os.Exit(1)
	}

	switch args[0] {
	case "sync":
		handleCostsSync(profile)
	case "summary":
		handleCostsSummary(profile, args[1:])
	case "export":
		handleCostsExport(profile, args[1:])
	case "recompute":
		handleCostsRecompute(profile, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown costs subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, costsUsage)
		os.Exit(1)
	}
}

// openCostStore creates a cost store from the profile's database.
func openCostStore(profile string) (*costs.Store, *session.Storage) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open storage: %v\n", err)
		os.Exit(1)
	}
	db := storage.GetDB()
	if db == nil {
		fmt.Fprintln(os.Stderr, "Error: database not available")
		os.Exit(1)
	}
	return costs.NewStore(db.DB()), storage
}

// newPricerFromConfig creates a Pricer using the user's config overrides.
func newPricerFromConfig() *costs.Pricer {
	cfg, _ := session.LoadUserConfig()
	pricerCfg := costs.PricerConfig{}
	if cfg != nil && len(cfg.Costs.Pricing.Overrides) > 0 {
		pricerCfg.Overrides = make(map[string]costs.PriceOverride)
		for model, ov := range cfg.Costs.Pricing.Overrides {
			pricerCfg.Overrides[model] = costs.PriceOverride{
				InputPerMtok:      ov.InputPerMtok,
				OutputPerMtok:     ov.OutputPerMtok,
				CacheReadPerMtok:  ov.CacheReadPerMtok,
				CacheWritePerMtok: ov.CacheWritePerMtok,
			}
		}
	}
	return costs.NewPricer(pricerCfg)
}

func handleCostsSync(profile string) {
	costStore, storage := openCostStore(profile)
	defer storage.Close()
	pricer := newPricerFromConfig()

	instances, err := storage.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	var syncSessions []costs.SyncSession
	for _, inst := range instances {
		if inst.Tool != "claude" || inst.ClaudeSessionID == "" {
			continue
		}
		syncSessions = append(syncSessions, costs.SyncSession{
			InstanceID:      inst.ID,
			ClaudeSessionID: inst.ClaudeSessionID,
			ProjectPath:     inst.ProjectPath,
			Tool:            inst.Tool,
		})
	}

	if len(syncSessions) == 0 {
		fmt.Println("No Claude sessions found to sync.")
		return
	}

	fmt.Printf("Syncing cost data for %d Claude session(s)...\n", len(syncSessions))
	result := costs.SyncFromTranscripts(costStore, pricer, syncSessions)

	fmt.Printf("\nResults:\n")
	fmt.Printf("  Sessions scanned: %d\n", result.SessionsScanned)
	fmt.Printf("  Events imported:  %d\n", result.EventsImported)
	fmt.Printf("  Events skipped:   %d (already tracked)\n", result.EventsSkipped)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:           %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("    - %s\n", e)
		}
	}
}

func handleCostsSummary(profile string, args []string) {
	// #1101: --json output so a remote agent-deck can be queried over SSH and
	// its cost totals merged into the local TUI's status-line cost segment.
	fs := flag.NewFlagSet("costs summary", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	since := fs.String("since", "", "First UTC date (YYYY-MM-DD)")
	until := fs.String("until", "", "Last UTC date, inclusive (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	costStore, storage := openCostStore(profile)
	defer storage.Close()
	if *since != "" || *until != "" {
		from, to, err := parseCostDateRange(*since, *until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		summary, err := costStore.TotalRange(from, to)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		rows, err := costStore.Export(from, to, costs.GroupByDay, costProfileName(profile), newPricerFromConfig())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		var costUSD *float64
		known := true
		for _, row := range rows {
			if row.CostUSD == nil {
				known = false
			}
		}
		if known {
			v := float64(summary.TotalCostMicrodollars) / 1_000_000
			costUSD = &v
		}
		payload := map[string]interface{}{"since": from.Format("2006-01-02"), "until": to.AddDate(0, 0, -1).Format("2006-01-02"), "events": summary.EventCount, "input_tokens": summary.TotalInputTokens, "output_tokens": summary.TotalOutputTokens, "cache_read_tokens": summary.TotalCacheReadTokens, "cache_write_tokens": summary.TotalCacheWriteTokens, "cost_usd": costUSD}
		if *jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(payload)
			return
		}
		cost := "?"
		if costUSD != nil {
			cost = fmt.Sprintf("$%.2f", *costUSD)
		}
		fmt.Printf("Cost Summary (%s through %s):\n  Total: %s (%d events)\n", payload["since"], payload["until"], cost, summary.EventCount)
		return
	}

	today, _ := costStore.TotalToday()
	yesterday, _ := costStore.TotalYesterday()
	week, _ := costStore.TotalThisWeek()
	lastWeek, _ := costStore.TotalLastWeek()
	month, _ := costStore.TotalThisMonth()
	lastMonth, _ := costStore.TotalLastMonth()
	projected, _ := costStore.ProjectedMonthly()
	pricer := newPricerFromConfig()
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := todayStart.AddDate(0, 0, -(int(todayStart.Weekday())+6)%7)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	known := func(from, to time.Time) bool {
		value, err := costKnownForRange(costStore, from, to, pricer)
		return err == nil && value
	}
	remote := costs.RemoteCostSummary{
		CostTodayMicrodollars: today.TotalCostMicrodollars, CostTodayKnown: known(todayStart, now.Add(time.Nanosecond)),
		CostYesterdayMicrodollars: yesterday.TotalCostMicrodollars, CostYesterdayKnown: known(todayStart.AddDate(0, 0, -1), todayStart),
		CostThisWeekMicrodollars: week.TotalCostMicrodollars, CostThisWeekKnown: known(weekStart, now.Add(time.Nanosecond)),
		CostLastWeekMicrodollars: lastWeek.TotalCostMicrodollars, CostLastWeekKnown: known(weekStart.AddDate(0, 0, -7), weekStart),
		CostThisMonthMicrodollars: month.TotalCostMicrodollars, CostThisMonthKnown: known(monthStart, now.Add(time.Nanosecond)),
		CostLastMonthMicrodollars: lastMonth.TotalCostMicrodollars, CostLastMonthKnown: known(monthStart.AddDate(0, -1, 0), monthStart),
		CostProjectedMicrodollars: projected, CostProjectedKnown: known(now.AddDate(0, 0, -7), now.Add(time.Nanosecond)),
		EventsToday: today.EventCount, EventsThisWeek: week.EventCount, EventsThisMonth: month.EventCount,
	}

	if *jsonOutput {
		// Wire shape mirrors costs.RemoteCostSummary so SSHRunner can json.Unmarshal directly.
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(remote)
		return
	}

	displayCost := func(v int64, known bool) string {
		if !known {
			return "?"
		}
		return costs.FormatUSD(v)
	}
	fmt.Printf("Cost Summary:\n")
	fmt.Printf("  Today:      %s (%d events)\n", displayCost(today.TotalCostMicrodollars, remote.CostTodayKnown), today.EventCount)
	fmt.Printf("  This week:  %s (%d events)\n", displayCost(week.TotalCostMicrodollars, remote.CostThisWeekKnown), week.EventCount)
	fmt.Printf("  This month: %s (%d events)\n", displayCost(month.TotalCostMicrodollars, remote.CostThisMonthKnown), month.EventCount)
	fmt.Printf("  Projected:  %s/mo\n", displayCost(projected, remote.CostProjectedKnown))

	top, _ := costStore.TopSessionsByCost(5)
	if len(top) > 0 {
		fmt.Printf("\nTop Sessions:\n")
		for i, sc := range top {
			title := sc.SessionTitle
			if title == "" {
				title = sc.SessionID
			}
			fmt.Printf("  %d. %-30s %s (%d events)\n", i+1, title, costs.FormatUSD(sc.CostMicrodollars), sc.EventCount)
		}
	}

	byModel, _ := costStore.CostByModel()
	if len(byModel) > 0 {
		fmt.Printf("\nCost by Model:\n")
		for model, cost := range byModel {
			value := costs.FormatUSD(cost)
			if _, ok := pricer.GetPrice(model); !ok {
				value = "?"
			}
			fmt.Printf("  %-30s %s\n", model, value)
		}
	}
}

func parseCostDateRange(since, until string) (time.Time, time.Time, error) {
	from := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	var err error
	if since != "" {
		from, err = time.Parse("2006-01-02", since)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --since %q (want YYYY-MM-DD)", since)
		}
	}
	if until != "" {
		d, e := time.Parse("2006-01-02", until)
		if e != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --until %q (want YYYY-MM-DD)", until)
		}
		to = d.AddDate(0, 0, 1)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("--since must not be after --until")
	}
	return from, to, nil
}

func handleCostsExport(profile string, args []string) {
	fs := flag.NewFlagSet("costs export", flag.ExitOnError)
	since := fs.String("since", "", "First UTC date (YYYY-MM-DD)")
	until := fs.String("until", "", "Last UTC date, inclusive (YYYY-MM-DD)")
	jsonOutput := fs.Bool("json", false, "Output as JSON array")
	by := fs.String("by", "session", "Group by session, model, or day")
	if err := fs.Parse(args); err != nil {
		return
	}
	from, to, err := parseCostDateRange(*since, *until)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	group := costs.ExportGroup(*by)
	if group != costs.GroupBySession && group != costs.GroupByModel && group != costs.GroupByDay {
		fmt.Fprintf(os.Stderr, "Error: invalid --by %q (want session, model, or day)\n", *by)
		return
	}
	store, storage := openCostStore(profile)
	defer storage.Close()
	rows, err := store.Export(from, to, group, costProfileName(profile), newPricerFromConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	if err := writeCostExport(os.Stdout, rows, *jsonOutput); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
}

func costProfileName(profile string) string {
	if profile == "" {
		return session.DefaultProfile
	}
	return profile
}

func costKnownForRange(store *costs.Store, from, to time.Time, pricer *costs.Pricer) (bool, error) {
	rows, err := store.Export(from, to, costs.GroupByModel, "", pricer)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.CostUSD == nil {
			return false, nil
		}
	}
	return true, nil
}

func writeCostExport(w io.Writer, rows []costs.ExportRow, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tTITLE\tTOOL\tMODEL\tEVENTS\tINPUT\tOUTPUT\tCACHE READ\tCACHE WRITE\tCOST\tFIRST\tLAST")
	for _, row := range rows {
		identity := row.SessionID
		if row.Day != "" {
			identity = row.Day
		}
		if identity == "" {
			identity = row.Model
		}
		cost := "?"
		if row.CostUSD != nil {
			cost = fmt.Sprintf("$%.6f", *row.CostUSD)
		}
		models := strings.ReplaceAll(row.Model, ",", ", ")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", identity, row.Title, row.Tool, models, row.Events, row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens, cost, row.FirstTimestamp.Format(time.RFC3339), row.LastTimestamp.Format(time.RFC3339))
	}
	return tw.Flush()
}

func handleCostsRecompute(profile string, args []string) {
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "-h", "--help":
			fmt.Println("Usage: agent-deck costs recompute [--dry-run]")
			fmt.Println("\nRecalculate cost_microdollars for every cost_events row using current")
			fmt.Println("pricing data (defaults + user overrides). Rows whose model is unknown to")
			fmt.Println("the pricer are left untouched. Idempotent.")
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", a)
			fmt.Fprintln(os.Stderr, "Usage: agent-deck costs recompute [--dry-run]")
			os.Exit(1)
		}
	}

	costStore, storage := openCostStore(profile)
	defer storage.Close()
	pricer := newPricerFromConfig()

	if dryRun {
		fmt.Println("Recomputing cost_events (dry-run, no rows will be modified)...")
	} else {
		fmt.Println("Recomputing cost_events...")
	}

	updated, skipped, err := costs.Recompute(context.Background(), costStore, pricer, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nResults:\n")
	if dryRun {
		fmt.Printf("  Would update: %d\n", updated)
	} else {
		fmt.Printf("  Updated:      %d\n", updated)
	}
	fmt.Printf("  Skipped:      %d (already correct or unknown model)\n", skipped)
	if dryRun && updated > 0 {
		fmt.Println("\nRe-run without --dry-run to apply changes.")
	}
}
