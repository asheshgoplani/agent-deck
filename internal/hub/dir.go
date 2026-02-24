package hub

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetHubDir returns the hub data directory (~/.agent-deck/hub).
func GetHubDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".agent-deck", "hub"), nil
}
