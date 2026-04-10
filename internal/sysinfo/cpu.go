package sysinfo

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// cpuState holds the previous /proc/stat reading for delta calculation.
var cpuState struct {
	mu        sync.Mutex
	prevTotal uint64
	prevIdle  uint64
	hasData   bool
}

func collectCPU() CPUStat {
	switch runtime.GOOS {
	case "linux":
		return collectCPULinux()
	case "darwin":
		return collectCPUDarwin()
	default:
		return CPUStat{}
	}
}

// collectCPULinux reads /proc/stat and computes CPU% from tick deltas.
// First call returns 0% (needs two samples for a delta).
func collectCPULinux() CPUStat {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUStat{}
	}

	// First line: cpu  user nice system idle iowait irq softirq steal ...
	line := strings.SplitN(string(data), "\n", 2)[0]
	if !strings.HasPrefix(line, "cpu ") {
		return CPUStat{}
	}

	fields := strings.Fields(line)
	if len(fields) < 5 {
		return CPUStat{}
	}

	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return CPUStat{}
		}
		total += val
		if i == 4 { // idle is the 4th value (index 4 in fields, field index 3 in cpu values)
			idle = val
		}
	}

	cpuState.mu.Lock()
	defer cpuState.mu.Unlock()

	if !cpuState.hasData {
		cpuState.prevTotal = total
		cpuState.prevIdle = idle
		cpuState.hasData = true
		return CPUStat{Available: true, UsagePercent: 0}
	}

	deltaTotal := total - cpuState.prevTotal
	deltaIdle := idle - cpuState.prevIdle
	cpuState.prevTotal = total
	cpuState.prevIdle = idle

	if deltaTotal == 0 {
		return CPUStat{Available: true, UsagePercent: 0}
	}

	usage := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
	return CPUStat{Available: true, UsagePercent: usage}
}

// collectCPUDarwin uses `ps` to approximate CPU usage on macOS.
func collectCPUDarwin() CPUStat {
	out, err := exec.Command("ps", "-A", "-o", "%cpu=").Output()
	if err != nil {
		return CPUStat{}
	}

	var totalCPU float64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		val, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			continue
		}
		totalCPU += val
	}

	numCPU := float64(runtime.NumCPU())
	if numCPU == 0 {
		numCPU = 1
	}
	usage := totalCPU / numCPU
	if usage > 100 {
		usage = 100
	}

	return CPUStat{Available: true, UsagePercent: usage}
}

// ParseProcStat parses a /proc/stat content string for testing.
// Returns (total, idle, error).
func ParseProcStat(content string) (uint64, uint64, error) {
	line := strings.SplitN(content, "\n", 2)[0]
	if !strings.HasPrefix(line, "cpu ") {
		return 0, 0, fmt.Errorf("not a cpu line")
	}
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0, fmt.Errorf("too few fields")
	}

	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		total += val
		if i == 4 {
			idle = val
		}
	}
	return total, idle, nil
}
