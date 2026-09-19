package ingest

import (
	"os"
	"strconv"
	"strings"
)

// LoadAvg1 is the one-minute load average (0 when unreadable).
func LoadAvg1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
