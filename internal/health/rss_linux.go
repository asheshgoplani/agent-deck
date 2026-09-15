//go:build linux

package health

import (
	"os"
	"strconv"
	"strings"
)

func residentBytes() *uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return nil
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return nil
	}
	value := pages * uint64(os.Getpagesize())
	return &value
}
