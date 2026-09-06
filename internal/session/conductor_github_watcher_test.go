package session

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// Issue #2134: [conductor.github_watcher] is parsed but not acted on by the
// binary. Absent table means disabled and mode "log".

func decodeConductor(t *testing.T, src string) ConductorSettings {
	t.Helper()
	var cfg struct {
		Conductor ConductorSettings `toml:"conductor"`
	}
	if _, err := toml.Decode(src, &cfg); err != nil {
		t.Fatalf("decode: %v\nconfig:\n%s", err, src)
	}
	return cfg.Conductor
}

func TestGitHubWatcherSettings_DefaultsWhenAbsent(t *testing.T) {
	c := decodeConductor(t, "[conductor]\nheartbeat_interval = 15\n")
	if c.GitHubWatcher.Enabled {
		t.Fatalf("expected github_watcher.enabled=false when the table is absent")
	}
	if got := c.GitHubWatcher.EffectiveMode(); got != GitHubWatcherModeLog {
		t.Fatalf("expected default mode %q, got %q", GitHubWatcherModeLog, got)
	}
	if c.HeartbeatInterval == nil || *c.HeartbeatInterval != 15 {
		t.Fatalf("heartbeat_interval must still decode alongside the new table")
	}
}

func TestGitHubWatcherSettings_ParsesSubTable(t *testing.T) {
	c := decodeConductor(t, "[conductor]\nheartbeat_interval = 15\n\n[conductor.github_watcher]\nenabled = true\nmode = \"dispatch\"\n")
	if !c.GitHubWatcher.Enabled {
		t.Fatalf("expected enabled=true")
	}
	if got := c.GitHubWatcher.EffectiveMode(); got != GitHubWatcherModeDispatch {
		t.Fatalf("expected mode %q, got %q", GitHubWatcherModeDispatch, got)
	}
}

func TestGitHubWatcherSettings_EnabledWithoutModeDefaultsToLog(t *testing.T) {
	c := decodeConductor(t, "[conductor.github_watcher]\nenabled = true\n")
	if !c.GitHubWatcher.Enabled {
		t.Fatalf("expected enabled=true")
	}
	if got := c.GitHubWatcher.EffectiveMode(); got != GitHubWatcherModeLog {
		t.Fatalf("enabled without mode must default to %q, got %q", GitHubWatcherModeLog, got)
	}
}
