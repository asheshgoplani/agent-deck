package sysinfo

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func collectLoad() LoadStat {
	switch runtime.GOOS {
	case "linux":
		return collectLoadLinux()
	case "darwin":
		return collectLoadDarwin()
	default:
		return LoadStat{}
	}
}

// collectLoadLinux reads /proc/loadavg.
func collectLoadLinux() LoadStat {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return LoadStat{}
	}
	return ParseLoadavg(string(data))
}

// ParseLoadavg parses /proc/loadavg content. Exported for testing.
// Format: "0.50 0.35 0.30 1/234 5678"
func ParseLoadavg(content string) LoadStat {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) < 3 {
		return LoadStat{}
	}

	load1, err1 := strconv.ParseFloat(fields[0], 64)
	load5, err2 := strconv.ParseFloat(fields[1], 64)
	load15, err3 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return LoadStat{}
	}

	return LoadStat{
		Available: true,
		Load1:     load1,
		Load5:     load5,
		Load15:    load15,
	}
}

// collectLoadDarwin uses sysctl vm.loadavg.
func collectLoadDarwin() LoadStat {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return LoadStat{}
	}
	return parseSysctlLoadavg(string(out))
}

// parseSysctlLoadavg parses sysctl vm.loadavg output.
// Format: "{ 1.23 4.56 7.89 }"
func parseSysctlLoadavg(s string) LoadStat {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")

	fields := strings.Fields(s)
	if len(fields) < 3 {
		return LoadStat{}
	}

	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return LoadStat{}
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return LoadStat{}
	}
	load15, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return LoadStat{}
	}

	return LoadStat{
		Available: true,
		Load1:     load1,
		Load5:     load5,
		Load15:    load15,
	}
}

// FormatLoadavg formats load averages for display.
func FormatLoadavg(l LoadStat) string {
	if !l.Available {
		return ""
	}
	return fmt.Sprintf("%.2f %.2f %.2f", l.Load1, l.Load5, l.Load15)
}
