//go:build darwin

package docker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// extractKeychainCredential extracts a credential from the macOS Keychain and writes it to destPath.
// If no keychain entry exists (e.g. using ANTHROPIC_API_KEY), this is not an error.
// Uses Output() (stdout only) to avoid leaking credential data in error messages.
func extractKeychainCredential(service string, destPath string) error {
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// No keychain entry is normal (e.g. using API key auth).
		errMsg := stderr.String()
		if strings.Contains(errMsg, "could not be found") ||
			strings.Contains(errMsg, "SecKeychainSearchCopyNext") {
			return nil
		}
		return fmt.Errorf("reading keychain service %s: %s: %w", service, strings.TrimSpace(errMsg), err)
	}

	password := strings.TrimSpace(string(out))
	if password == "" {
		return nil
	}

	return os.WriteFile(destPath, []byte(password), 0o600)
}
