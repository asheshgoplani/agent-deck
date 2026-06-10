//go:build !race

package testutil

// RaceEnabled reports whether the binary was built with -race. See race_on.go.
const RaceEnabled = false
