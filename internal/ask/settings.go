package ask

import (
	"errors"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type settings struct {
	enabled        bool
	scopes         []string
	allowSandboxed bool
}

// loadSettings reads [ask] from config.toml. These are guard rails an agent
// can edit, not security boundaries (docs/ask.md "Threat model").
func loadSettings() settings {
	cfg, _ := session.LoadUserConfig()
	if cfg == nil {
		return settings{enabled: true, scopes: DefaultSecretScopes}
	}
	return settings{enabled: cfg.Ask.IsEnabled(), scopes: cfg.Ask.Scopes(), allowSandboxed: cfg.Ask.AllowSandboxed}
}

// pidAlive reports whether pid may still run (unknown pids count as alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
