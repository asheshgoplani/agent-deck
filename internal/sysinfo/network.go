package sysinfo

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// networkState holds previous counter values for delta calculation.
var networkState struct {
	mu       sync.Mutex
	prevRx   uint64
	prevTx   uint64
	prevTime time.Time
	hasData  bool
}

func collectNetwork() NetworkStat {
	switch runtime.GOOS {
	case "linux":
		return collectNetworkLinux()
	case "darwin":
		return collectNetworkDarwin()
	default:
		return NetworkStat{}
	}
}

// collectNetworkLinux reads /proc/net/dev and computes bytes/sec from deltas.
func collectNetworkLinux() NetworkStat {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return NetworkStat{}
	}

	rx, tx := ParseNetDev(string(data))
	return computeNetworkDelta(rx, tx)
}

// ParseNetDev parses /proc/net/dev and returns total rx/tx bytes across all
// non-loopback interfaces. Exported for testing.
func ParseNetDev(content string) (rxTotal, txTotal uint64) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ":") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		iface := strings.TrimSpace(parts[0])

		// Skip loopback
		if iface == "lo" {
			continue
		}

		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}

		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}

		rxTotal += rx
		txTotal += tx
	}
	return
}

// collectNetworkDarwin uses netstat -ib to get network counters on macOS.
func collectNetworkDarwin() NetworkStat {
	out, err := exec.Command("netstat", "-ib").Output()
	if err != nil {
		return NetworkStat{}
	}

	rx, tx := parseNetstatDarwin(string(out))
	return computeNetworkDelta(rx, tx)
}

// parseNetstatDarwin parses `netstat -ib` output on macOS.
// Returns total rx/tx bytes across non-loopback interfaces.
func parseNetstatDarwin(output string) (rxTotal, txTotal uint64) {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		return
	}

	// Find column indices from header
	header := lines[0]
	fields := strings.Fields(header)
	ibytesIdx, obytesIdx := -1, -1
	for i, f := range fields {
		switch f {
		case "Ibytes":
			ibytesIdx = i
		case "Obytes":
			obytesIdx = i
		}
	}
	if ibytesIdx == -1 || obytesIdx == -1 {
		return
	}

	seen := make(map[string]bool)
	for _, line := range lines[1:] {
		cols := strings.Fields(line)
		if len(cols) <= obytesIdx {
			continue
		}

		iface := cols[0]
		// Skip loopback and duplicate interface entries
		if iface == "lo0" || seen[iface] {
			continue
		}
		seen[iface] = true

		rx, err := strconv.ParseUint(cols[ibytesIdx], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(cols[obytesIdx], 10, 64)
		if err != nil {
			continue
		}

		rxTotal += rx
		txTotal += tx
	}
	return
}

// computeNetworkDelta calculates bytes/sec from cumulative counters.
func computeNetworkDelta(rx, tx uint64) NetworkStat {
	networkState.mu.Lock()
	defer networkState.mu.Unlock()

	now := time.Now()

	if !networkState.hasData {
		networkState.prevRx = rx
		networkState.prevTx = tx
		networkState.prevTime = now
		networkState.hasData = true
		return NetworkStat{Available: true}
	}

	elapsed := now.Sub(networkState.prevTime).Seconds()
	if elapsed <= 0 {
		return NetworkStat{Available: true}
	}

	rxDelta := rx - networkState.prevRx
	txDelta := tx - networkState.prevTx

	// Handle counter wraps (unlikely but possible on 32-bit counters)
	if rx < networkState.prevRx {
		rxDelta = rx // counter wrapped, use raw value as approximation
	}
	if tx < networkState.prevTx {
		txDelta = tx
	}

	networkState.prevRx = rx
	networkState.prevTx = tx
	networkState.prevTime = now

	return NetworkStat{
		Available:     true,
		RxBytesPerSec: float64(rxDelta) / elapsed,
		TxBytesPerSec: float64(txDelta) / elapsed,
	}
}

// FormatBytes formats a byte count for human-readable display.
func FormatBytes(bytes uint64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// FormatBytesPerSec formats bytes/sec for display.
func FormatBytesPerSec(bps float64) string {
	return FormatBytes(uint64(bps)) + "/s"
}
