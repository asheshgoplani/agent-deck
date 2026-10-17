package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func runCostsDashboard(profile, command string, args []string) error {
	fs := flag.NewFlagSet("costs "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	days, limit, period := 30, 100, "all"
	usage := "Usage: agent-deck costs " + command + " --json"
	switch command {
	case "daily":
		fs.IntVar(&days, "days", 30, "Number of days (1-365)")
		usage += " [--days N] (default 30, 1-365)"
	case "sessions":
		fs.IntVar(&limit, "limit", 100, "Maximum sessions (1-500)")
		usage += " [--limit N] [--period today|7d|30d|all|week|month] (default limit 100, maximum 500; period all)"
	}
	if command == "sessions" || command == "models" || command == "groups" {
		fs.StringVar(&period, "period", "all", "today|7d|30d|all|week|month")
		if command != "sessions" {
			usage += " [--period today|7d|30d|all|week|month] (default all)"
		}
	}
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, usage)
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if !*jsonOutput {
		return errors.New(usage)
	}
	if days < 1 || days > 365 {
		return errors.New("days must be between 1 and 365")
	}
	if limit < 1 || limit > 500 {
		return errors.New("limit must be between 1 and 500")
	}
	if err := costs.ValidateCostPeriod(period); err != nil {
		return err
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return err
	}
	if !session.ResolveCostTrackingEnabled(cfg, session.GetEffectiveProfile(profile)) {
		return errors.New("cost tracking is off in this profile")
	}
	store, storage := openCostStore(profile)
	defer storage.Close()
	var payload any
	switch command {
	case "daily":
		payload, err = store.DashboardDaily(days)
	case "sessions":
		payload, err = store.DashboardSessions(limit, period)
	case "models":
		payload, err = store.DashboardModels(period)
	case "groups":
		payload, err = store.DashboardGroups(period)
	case "budgets":
		var budgetCfg costs.BudgetConfig
		budgetCfg, err = costBudgetConfig(cfg)
		if err == nil {
			payload, err = costs.NewBudgetChecker(budgetCfg, store).Dashboard()
		}
	default:
		return fmt.Errorf("unknown costs subcommand: %s", command)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(payload)
}

// costBudgetConfig keeps the dashboard and enforcement on the same settings.
func costBudgetConfig(cfg *session.UserConfig) (costs.BudgetConfig, error) {
	result := costs.BudgetConfig{}
	if cfg == nil {
		return result, nil
	}
	settings := cfg.Costs.Budgets
	result.DailyLimit = int64(math.Round(settings.DailyLimit * 1_000_000))
	result.WeeklyLimit = int64(math.Round(settings.WeeklyLimit * 1_000_000))
	result.MonthlyLimit = int64(math.Round(settings.MonthlyLimit * 1_000_000))
	result.GroupLimits = make(map[string]int64, len(settings.Groups))
	for name, budget := range settings.Groups {
		result.GroupLimits[name] = int64(math.Round(budget.DailyLimit * 1_000_000))
	}
	result.SessionLimits = make(map[string]int64, len(settings.Sessions))
	for id, budget := range settings.Sessions {
		result.SessionLimits[id] = int64(math.Round(budget.TotalLimit * 1_000_000))
	}
	var err error
	result.Timezone, err = time.LoadLocation(cfg.Costs.GetTimezone())
	if err != nil {
		return result, fmt.Errorf("costs timezone: %w", err)
	}
	return result, nil
}
