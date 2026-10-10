package session

import (
	"os"
	"path/filepath"
	"strings"
)

func agentDeckBinaryPath() string {
	// In production this should resolve to the installed binary.
	if p := FindAgentDeck(); p != "" {
		return p
	}

	// Fall back to current executable only if it looks like the agent-deck binary.
	if exe, err := os.Executable(); err == nil {
		base := strings.ToLower(filepath.Base(exe))
		if strings.HasPrefix(base, "agent-deck") {
			return exe
		}
	}

	return "agent-deck"
}
