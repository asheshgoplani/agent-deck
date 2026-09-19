package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionRecent is the CLI counterpart to the TUI's alternate-session
// toggle and MRU walk (#2058): it lists sessions ordered by the persisted
// last_accessed column, most-recently-used first. Unlike the TUI's in-memory
// MRUHistory ring (internal/session/mru.go), this reads straight from
// storage — a separate process has no access to a running TUI's ring — which
// is also what makes the ordering survive a restart.
func handleSessionRecent(profile string, args []string) {
	fs := flag.NewFlagSet("session recent", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	limit := fs.Int("limit", 20, "Maximum number of sessions to list (0 = no limit)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session recent [options]")
		fmt.Println()
		fmt.Println("List sessions most-recently-used first, backed by the same")
		fmt.Println("last_accessed column as the TUI's alternate-session toggle (`)")
		fmt.Println("and MRU walk (Alt+Left / Alt+Right). Never-attached sessions are")
		fmt.Println("omitted: there is nothing to rank them by.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session recent")
		fmt.Println("  agent-deck session recent --json --limit 5")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	recent := make([]*session.Instance, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.LastAccessedAt.IsZero() {
			continue
		}
		recent = append(recent, inst)
	}
	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].LastAccessedAt.After(recent[j].LastAccessedAt)
	})
	if *limit > 0 && len(recent) > *limit {
		recent = recent[:*limit]
	}

	if *jsonOutput {
		rows := make([]map[string]interface{}, 0, len(recent))
		for _, inst := range recent {
			rows = append(rows, map[string]interface{}{
				"id":            inst.ID,
				"title":         inst.Title,
				"tool":          inst.Tool,
				"group_path":    inst.GroupPath,
				"last_accessed": inst.LastAccessedAt.Format(time.RFC3339),
			})
		}
		out.Success("", rows)
		return
	}

	if len(recent) == 0 {
		fmt.Println("No recently used sessions (nothing has been attached yet).")
		return
	}
	for _, inst := range recent {
		groupSuffix := ""
		if inst.GroupPath != "" {
			groupSuffix = " [" + inst.GroupPath + "]"
		}
		fmt.Printf("%s  %-30s %s%s\n", inst.LastAccessedAt.Local().Format("2006-01-02 15:04:05"), inst.Title, inst.ID, groupSuffix)
	}
}
