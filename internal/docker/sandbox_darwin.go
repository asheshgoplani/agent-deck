//go:build darwin

package docker

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// readKeychainSecret reads a generic password from the macOS Keychain.
// A missing entry (e.g. using ANTHROPIC_API_KEY) returns "" and no error.
// Uses Output() (stdout only) to avoid leaking credential data in error messages.
func readKeychainSecret(service string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// No keychain entry is normal (e.g. using API key auth).
		errMsg := stderr.String()
		if strings.Contains(errMsg, "could not be found") ||
			strings.Contains(errMsg, "SecKeychainSearchCopyNext") {
			return "", nil
		}
		return "", fmt.Errorf("reading keychain service %s: %s: %w", service, strings.TrimSpace(errMsg), err)
	}
	return strings.TrimSpace(string(out)), nil
}
