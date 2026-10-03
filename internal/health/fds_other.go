//go:build !linux && !darwin

package health

// openFDs reports that this platform has no native descriptor count, so the
// descriptor budget is shown as unsupported instead of silently unchecked.
func openFDs() (*int, bool) { return nil, false }
