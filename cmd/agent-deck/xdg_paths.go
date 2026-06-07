package main

import (
	"os"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func effectiveUserConfigPathForHelp() string {
	path, err := session.GetUserConfigPath()
	if err != nil {
		return "config.toml"
	}
	return path
}

func effectiveCacheDir() (string, error) {
	return agentpaths.CacheDir()
}

func ensureEffectiveCacheDir() (string, error) {
	dir, err := effectiveCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func effectiveCachePath(name string) (string, error) {
	return agentpaths.CachePath(name)
}
