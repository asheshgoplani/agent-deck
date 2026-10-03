//go:build windows

package update

// dirWritable is only consulted for systemd units, which Windows has none
// of; a rename that fails still reports its own error.
func dirWritable(string) bool { return true }
