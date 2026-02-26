//go:build !darwin

package docker

// extractKeychainCredential is a no-op on non-macOS platforms.
// On Linux, credentials live in the config directory and are copied into the sandbox.
func extractKeychainCredential(_ string, _ string) error {
	return nil
}
