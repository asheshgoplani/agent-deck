package session

// MergePanelConfigOntoDisk loads the on-disk UserConfig and overlays the
// subset of fields that the TUI settings panel and setup wizard manage.
// Every other top-level field — Remotes, Hotkeys, Plugins, Conductors,
// Groups, Notifications, OpenClaw, Costs, Watcher, Shell, etc. — is
// preserved verbatim from disk.
//
// Issue #1067 fix. Previously, SettingsPanel.GetConfig and
// SetupWizard.GetConfig built fresh UserConfig values and home.go saved
// them directly. Any top-level field the panel did not explicitly copy
// from originalConfig was silently wiped — the reporter saw this as
// "remotes disappeared after Ctrl+C exit" because the most common path
// that triggers the save is opening Settings during a session.
//
// The fix inverts the data flow: instead of "construct fresh + manually
// preserve a few fields", we "start from disk + overlay panel-managed
// fields". New top-level UserConfig fields are now safe-by-default —
// they survive panel saves unless explicitly listed here.
//
// Callers (home.go settings + setup wizard paths) should replace
//
//	if err := SaveUserConfig(panel.GetConfig()); err != nil { ... }
//
// with
//
//	merged, err := session.MergePanelConfigOntoDisk(panel.GetConfig())
//	if err == nil { _ = session.SaveUserConfig(merged) }
//
// to inherit the preservation guarantee.
func MergePanelConfigOntoDisk(panel *UserConfig) (*UserConfig, error) {
	base, err := LoadUserConfig()
	if err != nil {
		return nil, err
	}
	// Shallow copy so we don't mutate the LoadUserConfig cache.
	merged := *base
	if panel == nil {
		return &merged, nil
	}

	// ── Panel-managed top-level scalars ────────────────────────────────
	merged.Theme = panel.Theme
	merged.DefaultTool = panel.DefaultTool

	// ── Gemini / Codex (panel manages YoloMode only) ───────────────────
	merged.Gemini.YoloMode = panel.Gemini.YoloMode
	merged.Codex.YoloMode = panel.Codex.YoloMode

	// ── Updates (panel manages CheckEnabled + AutoUpdate) ──────────────
	merged.Updates.CheckEnabled = panel.Updates.CheckEnabled
	merged.Updates.AutoUpdate = panel.Updates.AutoUpdate

	// ── Logs (panel manages 3 fields; other Logs.* preserved) ──────────
	merged.Logs.MaxSizeMB = panel.Logs.MaxSizeMB
	merged.Logs.MaxLines = panel.Logs.MaxLines
	merged.Logs.RemoveOrphans = panel.Logs.RemoveOrphans

	// ── GlobalSearch ───────────────────────────────────────────────────
	merged.GlobalSearch.Enabled = panel.GlobalSearch.Enabled
	merged.GlobalSearch.Tier = panel.GlobalSearch.Tier
	merged.GlobalSearch.RecentDays = panel.GlobalSearch.RecentDays

	// ── Maintenance.Enabled (only field panel manages) ─────────────────
	merged.Maintenance.Enabled = panel.Maintenance.Enabled

	// ── Claude subset (DangerousMode + ConfigDir; ExtraArgs, AutoMode,
	//    UseChrome, HooksEnabled, etc. preserved from disk) ─────────────
	if panel.Claude.DangerousMode != nil {
		merged.Claude.DangerousMode = panel.Claude.DangerousMode
	}
	// Panel deliberately writes "" when the user wants to clear the field.
	merged.Claude.ConfigDir = panel.Claude.ConfigDir

	// ── Preview subset (panel manages Show* + NotesOutputSplit; the
	//    nested Analytics sub-table stays from disk) ───────────────────
	if panel.Preview.ShowOutput != nil {
		merged.Preview.ShowOutput = panel.Preview.ShowOutput
	}
	if panel.Preview.ShowAnalytics != nil {
		merged.Preview.ShowAnalytics = panel.Preview.ShowAnalytics
	}
	if panel.Preview.ShowNotes != nil {
		merged.Preview.ShowNotes = panel.Preview.ShowNotes
	}
	if panel.Preview.NotesOutputSplit > 0 {
		merged.Preview.NotesOutputSplit = panel.Preview.NotesOutputSplit
	}

	// ── Display subset (panel manages ShowSessionTimestamps; FullRepaint
	//    and filter prefs stay from disk) ───────────────────────────────
	merged.Display.ShowSessionTimestamps = panel.Display.ShowSessionTimestamps

	// ── SystemStats subset ─────────────────────────────────────────────
	if panel.SystemStats.Enabled != nil {
		merged.SystemStats.Enabled = panel.SystemStats.Enabled
	}
	if panel.SystemStats.RefreshSeconds > 0 {
		merged.SystemStats.RefreshSeconds = panel.SystemStats.RefreshSeconds
	}
	if panel.SystemStats.Format != "" {
		merged.SystemStats.Format = panel.SystemStats.Format
	}
	if len(panel.SystemStats.Show) > 0 {
		merged.SystemStats.Show = panel.SystemStats.Show
	}

	// ── Profile overlay (panel manages per-profile Claude.ConfigDir) ───
	if len(panel.Profiles) > 0 {
		if merged.Profiles == nil {
			merged.Profiles = make(map[string]ProfileSettings)
		}
		for k, v := range panel.Profiles {
			merged.Profiles[k] = v
		}
	}

	return &merged, nil
}
