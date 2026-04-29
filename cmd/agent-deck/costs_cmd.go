package main

import (
	"context"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const costsUsage = "Usage: agent-deck costs <sync|summary|recompute>"

func handleCosts(profile string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, costsUsage)
		os.Exit(1)
	}

	switch args[0] {
	case "sync":
		handleCostsSync(profile)
	case "summary":
		handleCostsSummary(profile)
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

func handleCostsSummary(profile string) {
	costStore, storage := openCostStore(profile)
	defer storage.Close()

	today, _ := costStore.TotalToday()
	week, _ := costStore.TotalThisWeek()
	month, _ := costStore.TotalThisMonth()
	projected, _ := costStore.ProjectedMonthly()

	fmt.Printf("Cost Summary:\n")
	fmt.Printf("  Today:      %s (%d events)\n", costs.FormatUSD(today.TotalCostMicrodollars), today.EventCount)
	fmt.Printf("  This week:  %s (%d events)\n", costs.FormatUSD(week.TotalCostMicrodollars), week.EventCount)
	fmt.Printf("  This month: %s (%d events)\n", costs.FormatUSD(month.TotalCostMicrodollars), month.EventCount)
	fmt.Printf("  Projected:  %s/mo\n", costs.FormatUSD(projected))

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
			fmt.Printf("  %-30s %s\n", model, costs.FormatUSD(cost))
		}
	}
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
