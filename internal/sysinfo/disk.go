package sysinfo

import "syscall"

// collectDisk gets root filesystem usage via Statfs (cross-platform).
func collectDisk() DiskStat {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return DiskStat{}
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize) // #nosec G115 -- statfs Bsize is positive on real filesystems
	freeBytes := stat.Bavail * uint64(stat.Bsize)  // #nosec G115 -- statfs Bsize is positive on real filesystems; Bavail = available to non-root
	usedBytes := totalBytes - freeBytes

	var pct float64
	if totalBytes > 0 {
		pct = float64(usedBytes) / float64(totalBytes) * 100
	}

	return DiskStat{
		Available:    true,
		UsedBytes:    usedBytes,
		TotalBytes:   totalBytes,
		UsagePercent: pct,
	}
}
