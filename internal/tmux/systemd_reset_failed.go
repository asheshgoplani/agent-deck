package tmux

import "strings"

// resetFailedReportsCollectedUnit recognizes only systemctl's complete reply
// for the exact unit being absent after collection. All other reset-failed
// errors, including manager and authorization failures, remain fatal.
func resetFailedReportsCollectedUnit(output []byte, unit string) bool {
	expected := "Failed to reset failed state of unit " + unit + ": Unit " + unit + " not loaded."
	return strings.TrimSpace(string(output)) == expected
}
