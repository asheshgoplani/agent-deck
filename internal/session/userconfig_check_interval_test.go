package session

import (
	"testing"
	"time"
)

// The near-event-driven poll: [updates].check_interval (a duration string,
// default 90s) is independent of check_interval_hours (which only throttles
// the legacy byte-push sweep), and sweep_remotes defaults to false so a
// fresh install nudges remotes instead of pushing bytes onto them.
func TestUpdateSettings_CheckInterval(t *testing.T) {
	t.Run("default is 90s", func(t *testing.T) {
		setupSessionXDGPathEnv(t)
		got := GetUpdateSettings().GetCheckInterval()
		if got != DefaultCheckInterval {
			t.Fatalf("GetCheckInterval() = %v, want default %v", got, DefaultCheckInterval)
		}
		if DefaultCheckInterval != 90*time.Second {
			t.Fatalf("DefaultCheckInterval = %v, want 90s", DefaultCheckInterval)
		}
	})

	t.Run("configured value parses", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
check_interval = "45s"
`)
		got := GetUpdateSettings().GetCheckInterval()
		if got != 45*time.Second {
			t.Fatalf("GetCheckInterval() = %v, want 45s", got)
		}
	})

	t.Run("unparsable value falls back to default", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
check_interval = "banana"
`)
		got := GetUpdateSettings().GetCheckInterval()
		if got != DefaultCheckInterval {
			t.Fatalf("GetCheckInterval() = %v, want the default %v for an unparsable value", got, DefaultCheckInterval)
		}
	})

	t.Run("zero or negative value falls back to default", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
check_interval = "-5s"
`)
		got := GetUpdateSettings().GetCheckInterval()
		if got != DefaultCheckInterval {
			t.Fatalf("GetCheckInterval() = %v, want the default %v for a non-positive value", got, DefaultCheckInterval)
		}
	})

	t.Run("check_interval_hours is unrelated", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
check_interval_hours = 6
`)
		settings := GetUpdateSettings()
		if settings.GetCheckInterval() != DefaultCheckInterval {
			t.Fatalf("GetCheckInterval() = %v, want the default %v; check_interval_hours must not affect it", settings.GetCheckInterval(), DefaultCheckInterval)
		}
		if settings.CheckIntervalHours != 6 {
			t.Fatalf("CheckIntervalHours = %d, want 6", settings.CheckIntervalHours)
		}
	})
}

func TestUpdateSettings_SweepRemotes(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		setupSessionXDGPathEnv(t)
		settings := GetUpdateSettings()
		if settings.GetSweepRemotes() {
			t.Fatal("sweep_remotes must default to false: remotes are nudged, not pushed to")
		}
		if settings.SweepRemotes != nil {
			t.Fatal("an unset key must stay nil so the default can change without rewriting configs")
		}
	})

	t.Run("opt in", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
sweep_remotes = true
`)
		if !GetUpdateSettings().GetSweepRemotes() {
			t.Fatal("sweep_remotes = true must enable the byte-pushing sweep")
		}
	})

	t.Run("explicit opt out", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
sweep_remotes = false
`)
		if GetUpdateSettings().GetSweepRemotes() {
			t.Fatal("sweep_remotes = false must stay off")
		}
	})
}
