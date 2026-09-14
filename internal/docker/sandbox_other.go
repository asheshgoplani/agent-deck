//go:build !darwin

package docker

// readKeychainSecret is a no-op on non-macOS platforms.
// On Linux, credentials live in the config directory and are copied into the sandbox.
func readKeychainSecret(_ string) (string, error) {
	return "", nil
}
