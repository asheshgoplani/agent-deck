//go:build !darwin

package main

import (
	"fmt"
	"os"
)

// runMenubar is macOS-only. On other platforms it exits with a clear message
// so cross-platform builds and CI stay green.
func runMenubar(_ string, _ []string) {
	fmt.Fprintln(os.Stderr, "agent-deck menubar is only supported on macOS")
	os.Exit(1)
}
