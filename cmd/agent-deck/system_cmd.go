package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
)

// handleSystem dispatches `agent-deck system <subcommand>`.
func handleSystem(args []string) {
	if len(args) == 0 || helpRequested(args) {
		fmt.Println("Usage: agent-deck system <subcommand>")
		fmt.Println("\nSubcommands:")
		fmt.Println("  stats    Print this host's CPU/memory/disk/load snapshot")
		return
	}
	switch args[0] {
	case "stats":
		handleSystemStats(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown system subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// systemStatsJSON is the wire shape of `agent-deck system stats --json`: the
// remote preview panel's stats block (#2276 follow-up) polls this over SSH
// the same way it polls `list --json`, so it must stay byte-stable and cheap
// — sysinfo.Collect() is the same one-shot snapshot the web UI's
// /api/system/stats handler already serves, never a controller-side ssh to
// /proc.
type systemStatsJSON struct {
	CPU *struct {
		UsagePercent float64 `json:"usage_percent"`
	} `json:"cpu,omitempty"`
	Load *struct {
		Load1  float64 `json:"load1"`
		Load5  float64 `json:"load5"`
		Load15 float64 `json:"load15"`
	} `json:"load,omitempty"`
	Memory *struct {
		UsedBytes    uint64  `json:"used_bytes"`
		TotalBytes   uint64  `json:"total_bytes"`
		UsagePercent float64 `json:"usage_percent"`
	} `json:"memory,omitempty"`
	Disk *struct {
		UsedBytes    uint64  `json:"used_bytes"`
		TotalBytes   uint64  `json:"total_bytes"`
		UsagePercent float64 `json:"usage_percent"`
	} `json:"disk,omitempty"`
}

// handleSystemStats implements `agent-deck system stats --json`, printing a
// snapshot from the same sysinfo collector the web UI uses. Fields for a
// stat that could not be collected on this host (wrong platform, missing
// /proc, ...) are simply omitted, matching handleSystemStats in
// internal/web/handlers_system.go so both callers degrade the same way.
func handleSystemStats(args []string) {
	fs := flag.NewFlagSet("system stats", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	_ = fs.Parse(args)

	stats := sysinfo.Collect()

	if !*jsonOutput {
		if stats.CPU.Available {
			fmt.Printf("CPU:    %.0f%%\n", stats.CPU.UsagePercent)
		}
		if stats.Load.Available {
			fmt.Printf("Load:   %.2f  %.2f  %.2f\n", stats.Load.Load1, stats.Load.Load5, stats.Load.Load15)
		}
		if stats.Memory.Available {
			fmt.Printf("Memory: %s/%s (%.0f%%)\n", sysinfo.FormatBytes(stats.Memory.UsedBytes), sysinfo.FormatBytes(stats.Memory.TotalBytes), stats.Memory.UsagePercent)
		}
		if stats.Disk.Available {
			fmt.Printf("Disk:   %s/%s (%.0f%%)\n", sysinfo.FormatBytes(stats.Disk.UsedBytes), sysinfo.FormatBytes(stats.Disk.TotalBytes), stats.Disk.UsagePercent)
		}
		return
	}

	out := systemStatsJSON{}
	if stats.CPU.Available {
		out.CPU = &struct {
			UsagePercent float64 `json:"usage_percent"`
		}{UsagePercent: stats.CPU.UsagePercent}
	}
	if stats.Load.Available {
		out.Load = &struct {
			Load1  float64 `json:"load1"`
			Load5  float64 `json:"load5"`
			Load15 float64 `json:"load15"`
		}{Load1: stats.Load.Load1, Load5: stats.Load.Load5, Load15: stats.Load.Load15}
	}
	if stats.Memory.Available {
		out.Memory = &struct {
			UsedBytes    uint64  `json:"used_bytes"`
			TotalBytes   uint64  `json:"total_bytes"`
			UsagePercent float64 `json:"usage_percent"`
		}{UsedBytes: stats.Memory.UsedBytes, TotalBytes: stats.Memory.TotalBytes, UsagePercent: stats.Memory.UsagePercent}
	}
	if stats.Disk.Available {
		out.Disk = &struct {
			UsedBytes    uint64  `json:"used_bytes"`
			TotalBytes   uint64  `json:"total_bytes"`
			UsagePercent float64 `json:"usage_percent"`
		}{UsedBytes: stats.Disk.UsedBytes, TotalBytes: stats.Disk.TotalBytes, UsagePercent: stats.Disk.UsagePercent}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Printf("Error: failed to format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
