package cchook

import (
	"os"
	"path/filepath"
	"runtime"
)

func DefaultUserClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func DefaultManagedDir() string {
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/claude-code"
	}
	return "/etc/claude-code"
}
