package sysinfo

import (
	"fmt"
	"strings"
)

// Format renders stats as a string for the tmux status bar.
// format: "compact" (icons+values), "full" (labels+values), "minimal" (values only)
// show: which stats to include (e.g., ["cpu", "ram", "disk"])
func Format(stats Stats, format string, show []string) string {
	showSet := make(map[string]bool, len(show))
	for _, s := range show {
		showSet[s] = true
	}

	var parts []string

	if showSet["cpu"] && stats.CPU.Available {
		parts = append(parts, formatCPU(stats.CPU, format))
	}
	if showSet["ram"] && stats.Memory.Available {
		parts = append(parts, formatMem(stats.Memory, format))
	}
	if showSet["disk"] && stats.Disk.Available {
		parts = append(parts, formatDisk(stats.Disk, format))
	}
	if showSet["load"] && stats.Load.Available {
		parts = append(parts, formatLoad(stats.Load, format))
	}
	if showSet["gpu"] && stats.GPU.Available {
		parts = append(parts, formatGPU(stats.GPU, format))
	}
	if showSet["network"] && stats.Network.Available {
		if stats.Network.RxBytesPerSec > 0 || stats.Network.TxBytesPerSec > 0 {
			parts = append(parts, formatNet(stats.Network, format))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, " │ ")
}

func formatCPU(s CPUStat, format string) string {
	switch format {
	case "full":
		return fmt.Sprintf("CPU: %.0f%%", s.UsagePercent)
	case "minimal":
		return fmt.Sprintf("%.0f%%", s.UsagePercent)
	default: // compact
		return fmt.Sprintf("⚙ %.0f%%", s.UsagePercent)
	}
}

func formatMem(s MemStat, format string) string {
	used := FormatBytes(s.UsedBytes)
	total := FormatBytes(s.TotalBytes)
	switch format {
	case "full":
		return fmt.Sprintf("RAM: %s/%s (%.0f%%)", used, total, s.UsagePercent)
	case "minimal":
		return fmt.Sprintf("%s/%s", used, total)
	default: // compact
		return fmt.Sprintf("⛁ %s/%s", used, total)
	}
}

func formatDisk(s DiskStat, format string) string {
	used := FormatBytes(s.UsedBytes)
	total := FormatBytes(s.TotalBytes)
	switch format {
	case "full":
		return fmt.Sprintf("Disk: %s/%s (%.0f%%)", used, total, s.UsagePercent)
	case "minimal":
		return fmt.Sprintf("%.0f%%", s.UsagePercent)
	default: // compact
		return fmt.Sprintf("▪ %s/%s", used, total)
	}
}

func formatLoad(s LoadStat, format string) string {
	switch format {
	case "full":
		return fmt.Sprintf("Load: %.2f %.2f %.2f", s.Load1, s.Load5, s.Load15)
	case "minimal":
		return fmt.Sprintf("%.2f", s.Load1)
	default: // compact
		return fmt.Sprintf("↑ %.2f", s.Load1)
	}
}

func formatGPU(s GPUStat, format string) string {
	switch format {
	case "full":
		return fmt.Sprintf("GPU: %.0f%%", s.UsagePercent)
	case "minimal":
		return fmt.Sprintf("%.0f%%", s.UsagePercent)
	default: // compact
		return fmt.Sprintf("◈ %.0f%%", s.UsagePercent)
	}
}

func formatNet(s NetworkStat, format string) string {
	rx := FormatBytesPerSec(s.RxBytesPerSec)
	tx := FormatBytesPerSec(s.TxBytesPerSec)
	switch format {
	case "full":
		return fmt.Sprintf("Net: ↓%s ↑%s", rx, tx)
	case "minimal":
		return fmt.Sprintf("↓%s ↑%s", rx, tx)
	default: // compact
		return fmt.Sprintf("⇅ ↓%s ↑%s", rx, tx)
	}
}
