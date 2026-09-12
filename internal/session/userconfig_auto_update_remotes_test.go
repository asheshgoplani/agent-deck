package session

import "testing"

// #2164: [updates] auto_update_remotes is on unless the user opts out with
// auto_update_remotes = false, and parses as a plain bool next to the
// existing auto_update key.
func TestUpdateSettings_AutoUpdateRemotes(t *testing.T) {
	t.Run("default on", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
auto_update = true
`)
		settings := GetUpdateSettings()
		if !settings.GetAutoUpdateRemotes() {
			t.Fatal("auto_update_remotes must default to true")
		}
		if settings.AutoUpdateRemotes != nil {
			t.Fatal("an unset key must stay nil so the default can change without rewriting configs")
		}
		if !settings.AutoUpdate {
			t.Fatal("auto_update must still parse")
		}
		if settings.CheckIntervalHours != 24 {
			t.Fatalf("check_interval_hours default = %d, want 24", settings.CheckIntervalHours)
		}
	})

	t.Run("no config at all", func(t *testing.T) {
		setupSessionXDGPathEnv(t)
		if !GetUpdateSettings().GetAutoUpdateRemotes() {
			t.Fatal("auto_update_remotes must be on without a config file")
		}
	})

	t.Run("opt out", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
auto_update_remotes = false
check_interval_hours = 6
`)
		settings := GetUpdateSettings()
		if settings.GetAutoUpdateRemotes() {
			t.Fatal("auto_update_remotes = false must opt out")
		}
		if settings.AutoUpdate {
			t.Fatal("auto_update_remotes must not touch auto_update")
		}
		if settings.CheckIntervalHours != 6 {
			t.Fatalf("check_interval_hours = %d, want 6", settings.CheckIntervalHours)
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
		writeUserConfig(t, xdgConfigHome, `
[updates]
auto_update_remotes = true
`)
		if !GetUpdateSettings().GetAutoUpdateRemotes() {
			t.Fatal("auto_update_remotes = true must parse")
		}
	})
}
