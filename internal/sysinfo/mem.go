package sysinfo

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func collectMemory() MemStat {
	switch runtime.GOOS {
	case "linux":
		return collectMemLinux()
	case "darwin":
		return collectMemDarwin()
	default:
		return MemStat{}
	}
}

// collectMemLinux reads /proc/meminfo for MemTotal and MemAvailable.
func collectMemLinux() MemStat {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemStat{}
	}
	return ParseMeminfo(string(data))
}

// ParseMeminfo parses /proc/meminfo content. Exported for testing.
func ParseMeminfo(content string) MemStat {
	var total, available uint64
	var foundTotal, foundAvailable bool

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseMeminfoValue(line)
			foundTotal = true
		} else if strings.HasPrefix(line, "MemAvailable:") {
			available = parseMeminfoValue(line)
			foundAvailable = true
		}
		if foundTotal && foundAvailable {
			break
		}
	}

	if !foundTotal {
		return MemStat{}
	}

	// Values from /proc/meminfo are in kB
	totalBytes := total * 1024
	availableBytes := available * 1024
	usedBytes := totalBytes - availableBytes

	var pct float64
	if totalBytes > 0 {
		pct = float64(usedBytes) / float64(totalBytes) * 100
	}

	return MemStat{
		Available:    true,
		UsedBytes:    usedBytes,
		TotalBytes:   totalBytes,
		UsagePercent: pct,
	}
}

// parseMeminfoValue extracts the numeric value from a /proc/meminfo line.
// Format: "MemTotal:       16384000 kB"
func parseMeminfoValue(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	val, _ := strconv.ParseUint(parts[1], 10, 64)
	return val
}

// collectMemDarwin uses sysctl hw.memsize and vm_stat to get memory info.
func collectMemDarwin() MemStat {
	// Total memory
	totalOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return MemStat{}
	}
	totalBytes, err := strconv.ParseUint(strings.TrimSpace(string(totalOut)), 10, 64)
	if err != nil {
		return MemStat{}
	}

	// vm_stat gives page counts. Parse free + inactive as "available".
	vmOut, err := exec.Command("vm_stat").Output()
	if err != nil {
		return MemStat{Available: true, TotalBytes: totalBytes}
	}

	pageSize := uint64(4096) // default macOS page size
	var freePages, inactivePages, speculativePages uint64

	for _, line := range strings.Split(string(vmOut), "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			// Parse page size from header: "...page size of 16384 bytes)"
			if idx := strings.Index(line, "page size of "); idx != -1 {
				sizeStr := line[idx+len("page size of "):]
				sizeStr = strings.TrimSuffix(strings.TrimSpace(sizeStr), " bytes)")
				if ps, err := strconv.ParseUint(sizeStr, 10, 64); err == nil {
					pageSize = ps
				}
			}
			continue
		}
		if strings.HasPrefix(line, "Pages free:") {
			freePages = parseVMStatValue(line)
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactivePages = parseVMStatValue(line)
		} else if strings.HasPrefix(line, "Pages speculative:") {
			speculativePages = parseVMStatValue(line)
		}
	}

	availableBytes := (freePages + inactivePages + speculativePages) * pageSize
	usedBytes := totalBytes - availableBytes
	if availableBytes > totalBytes {
		usedBytes = 0
	}

	var pct float64
	if totalBytes > 0 {
		pct = float64(usedBytes) / float64(totalBytes) * 100
	}

	return MemStat{
		Available:    true,
		UsedBytes:    usedBytes,
		TotalBytes:   totalBytes,
		UsagePercent: pct,
	}
}

// parseVMStatValue extracts the numeric value from a vm_stat line.
// Format: "Pages free:                             12345."
func parseVMStatValue(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return 0
	}
	valStr := strings.TrimSuffix(parts[len(parts)-1], ".")
	val, _ := strconv.ParseUint(valStr, 10, 64)
	return val
}
