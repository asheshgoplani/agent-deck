//go:build !linux && !darwin

package health

// getrusage reports peak RSS, which is not current resident memory. Preserve
// unknown on platforms without a supported native current-RSS observation.
func residentBytes() *uint64 { return nil }
