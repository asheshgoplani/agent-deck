package ingest

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// LoadAvg1 is the one-minute load average (0 when unreadable).
func LoadAvg1() float64 {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(raw) < 24 {
		return 0
	}
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }
	ld := binary.LittleEndian.Uint32(raw[0:4])
	fscale := binary.LittleEndian.Uint64(raw[16:24])
	if fscale == 0 {
		return 0
	}
	return float64(ld) / float64(fscale)
}
