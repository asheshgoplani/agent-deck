package session

import "testing"

// #2164: [updates] auto_update_remotes is off unless the user sets it, and
// parses as a plain bool next to the existing auto_update key.
func TestUpdateSettings_AutoUpdateRemotes(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
auto_update = true
`)
		settings := GetUpdateSettings()
		if settings.AutoUpdateRemotes {
			t.Fatal("auto_update_remotes must default to false")
		}
		if !settings.AutoUpdate {
			t.Fatal("auto_update must still parse")
		}
		if settings.CheckIntervalHours != 24 {
			t.Fatalf("check_interval_hours default = %d, want 24", settings.CheckIntervalHours)
		}
	})

	t.Run("parses true", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
auto_update_remotes = true
check_interval_hours = 6
`)
		settings := GetUpdateSettings()
		if !settings.AutoUpdateRemotes {
			t.Fatal("auto_update_remotes = true must parse")
		}
		if settings.AutoUpdate {
			t.Fatal("auto_update_remotes must not imply auto_update")
		}
		if settings.CheckIntervalHours != 6 {
			t.Fatalf("check_interval_hours = %d, want 6", settings.CheckIntervalHours)
		}
	})
}
