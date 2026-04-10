// Package sysinfo provides cross-platform system statistics collection
// for CPU, memory, disk, load average, GPU, and network throughput.
//
// All collection functions return an Available flag. When a stat cannot
// be collected (wrong platform, missing /proc, no GPU), Available is false
// and the caller should skip it silently.
package sysinfo

// Stats holds a snapshot of all system statistics.
type Stats struct {
	CPU     CPUStat
	Memory  MemStat
	Disk    DiskStat
	Load    LoadStat
	GPU     GPUStat
	Network NetworkStat
}

// CPUStat represents CPU usage as a percentage across all cores.
type CPUStat struct {
	Available    bool
	UsagePercent float64 // 0-100
}

// MemStat represents memory usage.
type MemStat struct {
	Available    bool
	UsedBytes    uint64
	TotalBytes   uint64
	UsagePercent float64 // 0-100
}

// DiskStat represents root filesystem disk usage.
type DiskStat struct {
	Available    bool
	UsedBytes    uint64
	TotalBytes   uint64
	UsagePercent float64 // 0-100
}

// LoadStat represents system load averages.
type LoadStat struct {
	Available bool
	Load1     float64
	Load5     float64
	Load15    float64
}

// GPUStat represents GPU utilization (best-effort).
type GPUStat struct {
	Available    bool
	UsagePercent float64 // 0-100
	Name         string  // e.g., "NVIDIA GeForce RTX 3090"
}

// NetworkStat represents network throughput calculated from counter deltas.
type NetworkStat struct {
	Available bool
	RxBytesPerSec float64
	TxBytesPerSec float64
}
