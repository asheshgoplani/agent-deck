package testutil

import (
	"os"
	"path/filepath"
)

// SetupTestHome points HOME/XDG directories at a temporary location so tests do
// not read or mutate the developer's real ~/.agent-deck state.
func SetupTestHome() (restore func(), err error) {
	tempHome, err := os.MkdirTemp("", "agentdeck-test-home-")
	if err != nil {
		return nil, err
	}

	oldHome, hadHome := os.LookupEnv("HOME")
	oldConfig, hadConfig := os.LookupEnv("XDG_CONFIG_HOME")
	oldCache, hadCache := os.LookupEnv("XDG_CACHE_HOME")
	oldState, hadState := os.LookupEnv("XDG_STATE_HOME")

	_ = os.Setenv("HOME", tempHome)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(tempHome, ".cache"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(tempHome, ".local", "state"))
	_ = os.MkdirAll(filepath.Join(tempHome, ".agent-deck"), 0o755)

	restore = func() {
		if hadHome {
			_ = os.Setenv("HOME", oldHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
		if hadConfig {
			_ = os.Setenv("XDG_CONFIG_HOME", oldConfig)
		} else {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		}
		if hadCache {
			_ = os.Setenv("XDG_CACHE_HOME", oldCache)
		} else {
			_ = os.Unsetenv("XDG_CACHE_HOME")
		}
		if hadState {
			_ = os.Setenv("XDG_STATE_HOME", oldState)
		} else {
			_ = os.Unsetenv("XDG_STATE_HOME")
		}
		_ = os.RemoveAll(tempHome)
	}
	return restore, nil
}
