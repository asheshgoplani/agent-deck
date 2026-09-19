//go:build !darwin && !linux

package ingest

// LoadAvg1 is unavailable here; the load check is a no-op.
func LoadAvg1() float64 { return 0 }
