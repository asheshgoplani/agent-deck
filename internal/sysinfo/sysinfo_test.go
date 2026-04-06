package sysinfo

import (
	"math"
	"runtime"
	"testing"
)

// --- CPU parsing tests ---

func TestParseProcStat(t *testing.T) {
	content := "cpu  10132153 290696 3084719 46828483 16683 0 25195 0 0 0\ncpu0 ..."
	total, idle, err := ParseProcStat(content)
	if err != nil {
		t.Fatalf("ParseProcStat: %v", err)
	}

	// total = sum of all fields: 10132153+290696+3084719+46828483+16683+0+25195+0+0+0
	expectedTotal := uint64(10132153 + 290696 + 3084719 + 46828483 + 16683 + 0 + 25195 + 0 + 0 + 0)
	expectedIdle := uint64(46828483) // 4th value

	if total != expectedTotal {
		t.Errorf("total = %d, want %d", total, expectedTotal)
	}
	if idle != expectedIdle {
		t.Errorf("idle = %d, want %d", idle, expectedIdle)
	}
}

func TestParseProcStat_Invalid(t *testing.T) {
	_, _, err := ParseProcStat("not a cpu line")
	if err == nil {
		t.Error("expected error for invalid input")
	}

	_, _, err = ParseProcStat("cpu ")
	if err == nil {
		t.Error("expected error for empty fields")
	}
}

// --- Memory parsing tests ---

func TestParseMeminfo(t *testing.T) {
	content := `MemTotal:       16384000 kB
MemFree:         2000000 kB
MemAvailable:    8000000 kB
Buffers:          500000 kB
Cached:          5000000 kB
`
	stat := ParseMeminfo(content)
	if !stat.Available {
		t.Fatal("stat should be available")
	}

	expectedTotal := uint64(16384000 * 1024)
	if stat.TotalBytes != expectedTotal {
		t.Errorf("TotalBytes = %d, want %d", stat.TotalBytes, expectedTotal)
	}

	expectedUsed := uint64((16384000 - 8000000) * 1024)
	if stat.UsedBytes != expectedUsed {
		t.Errorf("UsedBytes = %d, want %d", stat.UsedBytes, expectedUsed)
	}

	expectedPct := float64(16384000-8000000) / float64(16384000) * 100
	if math.Abs(stat.UsagePercent-expectedPct) > 0.1 {
		t.Errorf("UsagePercent = %.1f, want ~%.1f", stat.UsagePercent, expectedPct)
	}
}

func TestParseMeminfo_Missing(t *testing.T) {
	stat := ParseMeminfo("SomeOtherField: 1234 kB\n")
	if stat.Available {
		t.Error("stat should not be available when MemTotal is missing")
	}
}

// --- Load average parsing tests ---

func TestParseLoadavg(t *testing.T) {
	stat := ParseLoadavg("0.50 0.35 0.30 1/234 5678\n")
	if !stat.Available {
		t.Fatal("stat should be available")
	}
	if stat.Load1 != 0.50 {
		t.Errorf("Load1 = %f, want 0.50", stat.Load1)
	}
	if stat.Load5 != 0.35 {
		t.Errorf("Load5 = %f, want 0.35", stat.Load5)
	}
	if stat.Load15 != 0.30 {
		t.Errorf("Load15 = %f, want 0.30", stat.Load15)
	}
}

func TestParseLoadavg_Invalid(t *testing.T) {
	stat := ParseLoadavg("abc")
	if stat.Available {
		t.Error("stat should not be available for invalid input")
	}
}

// --- Network parsing tests ---

func TestParseNetDev(t *testing.T) {
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  123456   1000    0    0    0     0          0         0   123456   1000    0    0    0     0       0          0
  eth0: 1000000   5000    0    0    0     0          0         0  2000000   3000    0    0    0     0       0          0
wlan0:   500000   2000    0    0    0     0          0         0   300000   1000    0    0    0     0       0          0
`
	rx, tx := ParseNetDev(content)

	// Should skip lo, sum eth0 + wlan0
	expectedRx := uint64(1000000 + 500000)
	expectedTx := uint64(2000000 + 300000)

	if rx != expectedRx {
		t.Errorf("rx = %d, want %d", rx, expectedRx)
	}
	if tx != expectedTx {
		t.Errorf("tx = %d, want %d", tx, expectedTx)
	}
}

func TestParseNetDev_Empty(t *testing.T) {
	rx, tx := ParseNetDev("")
	if rx != 0 || tx != 0 {
		t.Errorf("empty input should return 0,0; got %d,%d", rx, tx)
	}
}

// --- Format tests ---

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1K"},
		{1536, "2K"},
		{1048576, "1.0M"},
		{1073741824, "1.0G"},
		{1610612736, "1.5G"},
	}
	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFormat_Compact(t *testing.T) {
	stats := Stats{
		CPU:    CPUStat{Available: true, UsagePercent: 45},
		Memory: MemStat{Available: true, UsedBytes: 8 << 30, TotalBytes: 16 << 30, UsagePercent: 50},
		Load:   LoadStat{Available: true, Load1: 2.50, Load5: 2.30, Load15: 2.10},
	}

	result := Format(stats, "compact", []string{"cpu", "ram", "load"})
	if result == "" {
		t.Fatal("Format returned empty string")
	}

	// Should contain CPU, RAM, and load sections
	if !containsAll(result, "⚙ 45%", "⛁ 8.0G/16.0G", "↑ 2.50") {
		t.Errorf("Format compact = %q, missing expected parts", result)
	}
}

func TestFormat_Full(t *testing.T) {
	stats := Stats{
		CPU: CPUStat{Available: true, UsagePercent: 75},
	}
	result := Format(stats, "full", []string{"cpu"})
	if result != "CPU: 75%" {
		t.Errorf("Format full = %q, want %q", result, "CPU: 75%")
	}
}

func TestFormat_Minimal(t *testing.T) {
	stats := Stats{
		CPU: CPUStat{Available: true, UsagePercent: 30},
	}
	result := Format(stats, "minimal", []string{"cpu"})
	if result != "30%" {
		t.Errorf("Format minimal = %q, want %q", result, "30%")
	}
}

func TestFormat_UnavailableSkipped(t *testing.T) {
	stats := Stats{
		CPU: CPUStat{Available: true, UsagePercent: 50},
		GPU: GPUStat{Available: false},
	}
	result := Format(stats, "compact", []string{"cpu", "gpu"})
	if result != "⚙ 50%" {
		t.Errorf("Format should skip unavailable GPU: got %q", result)
	}
}

func TestFormat_EmptyShow(t *testing.T) {
	stats := Stats{
		CPU: CPUStat{Available: true, UsagePercent: 50},
	}
	// Empty show list should show nothing for the given stats
	result := Format(stats, "compact", []string{"disk"})
	if result != "" {
		t.Errorf("Format with non-matching show should be empty: got %q", result)
	}
}

// --- Collector tests ---

func TestCollector_Get(t *testing.T) {
	c := NewCollector(5, nil)
	stats := c.Get()
	// Before Start(), stats should be zero-valued
	if stats.CPU.Available {
		t.Error("CPU should not be available before Start()")
	}
}

// --- Integration tests (real system data) ---

func TestCollect_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	stats := Collect()

	// On a real system, at least some stats should be available
	switch runtime.GOOS {
	case "linux":
		if !stats.Memory.Available {
			t.Error("Memory should be available on Linux")
		}
		if stats.Memory.TotalBytes == 0 {
			t.Error("TotalBytes should be non-zero on Linux")
		}
		if !stats.Load.Available {
			t.Error("Load should be available on Linux")
		}
		if !stats.Disk.Available {
			t.Error("Disk should be available on Linux")
		}
		if stats.Disk.TotalBytes == 0 {
			t.Error("Disk TotalBytes should be non-zero")
		}
	case "darwin":
		if !stats.Memory.Available {
			t.Error("Memory should be available on macOS")
		}
		if !stats.Load.Available {
			t.Error("Load should be available on macOS")
		}
		if !stats.Disk.Available {
			t.Error("Disk should be available on macOS")
		}
	}
}

func TestCollect_CPUDelta_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if runtime.GOOS != "linux" {
		t.Skip("CPU delta test requires Linux /proc/stat")
	}

	// First collection seeds the delta state
	stats1 := Collect()
	if !stats1.CPU.Available {
		t.Fatal("CPU should be available on Linux")
	}

	// Second collection should give a real delta
	stats2 := Collect()
	if !stats2.CPU.Available {
		t.Fatal("CPU should still be available")
	}
	// CPU usage should be 0-100 (we can't predict the value, just validate range)
	if stats2.CPU.UsagePercent < 0 || stats2.CPU.UsagePercent > 100 {
		t.Errorf("CPU usage %.1f%% out of range 0-100", stats2.CPU.UsagePercent)
	}
}

// --- Helpers ---

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
