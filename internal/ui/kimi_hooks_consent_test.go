package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestKimiHooksDialogDisclosesMutationAndDefaultsToSkip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ".kimi-code", "config.toml")
	dialog := NewConfirmDialog()
	dialog.ShowInstallKimiHooks(configPath, session.KimiHookEventsForInstall())

	if dialog.GetConfirmType() != ConfirmInstallKimiHooks {
		t.Fatalf("confirm type = %v, want ConfirmInstallKimiHooks", dialog.GetConfirmType())
	}
	if dialog.GetFocusedButton() != 1 {
		t.Fatalf("focused button = %d, want Skip", dialog.GetFocusedButton())
	}
	if dialog.GetTargetID() != configPath {
		t.Errorf("dialog config path = %q, want %q", dialog.GetTargetID(), configPath)
	}
	view := dialog.View()
	compact := strings.ReplaceAll(strings.Join(strings.Fields(plain(view)), ""), "│", "")
	for _, disclosed := range []string{
		"Kimi Code Hooks", "Config:", configPath, "agent-deck hook-handler",
		"user permissions", "JSON", "stdin", "SessionStart", "TurnStarted",
		"PostToolUseFailure", "StopFailure", "SessionEnd",
	} {
		if !strings.Contains(compact, strings.Join(strings.Fields(disclosed), "")) {
			t.Errorf("dialog does not disclose %q:\n%s", disclosed, view)
		}
	}
}

func cleanupKimiHooksHome(t *testing.T, home *Home) {
	t.Helper()
	home.cancel()
	if home.hookWatcher != nil {
		home.hookWatcher.Stop()
	}
	if home.storageWatcher != nil {
		home.storageWatcher.Close()
	}
	if home.storage != nil {
		_ = home.storage.Close()
	}
}

func TestKimiDetectionDoesNotWriteConfigBeforeConsent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		initial []byte
	}{
		{name: "missing config"},
		{name: "existing config", initial: []byte("default_model = \"kimi-code/k3\"\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
			t.Setenv("AGENTDECK_PROFILE", "kimi-consent-"+tt.name)
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "kimi"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)

			configPath := filepath.Join(homeDir, ".kimi-code", "config.toml")
			if tt.initial != nil {
				if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, tt.initial, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			previousWorkers := homeBackgroundWorkersEnabled
			homeBackgroundWorkersEnabled = true
			t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })
			stubLogMaintenance(t)
			previousDB := statedb.GetGlobal()
			statedb.SetGlobal(nil)
			t.Cleanup(func() { statedb.SetGlobal(previousDB) })

			home := NewHome()
			t.Cleanup(func() { cleanupKimiHooksHome(t, home) })
			if !home.pendingKimiHooksPrompt {
				t.Fatal("Kimi detection did not queue its consent prompt")
			}
			got, err := os.ReadFile(configPath)
			if tt.initial == nil {
				if !os.IsNotExist(err) {
					t.Fatalf("Kimi detection wrote config before consent; err=%v content=%q", err, got)
				}
				return
			}
			if err != nil || string(got) != string(tt.initial) {
				t.Fatalf("Kimi detection changed config before consent: got %q err=%v", got, err)
			}
		})
	}
}

func TestPendingHookPromptsAreSequencedHermesThenKimi(t *testing.T) {
	home := &Home{
		confirmDialog:            NewConfirmDialog(),
		pendingHermesHooksPrompt: true,
		pendingKimiHooksPrompt:   true,
	}
	home.showPendingHooksPrompt()
	if got := home.confirmDialog.GetConfirmType(); got != ConfirmInstallHermesHooks {
		t.Fatalf("first prompt = %v, want Hermes hooks", got)
	}
	home.pendingHermesHooksPrompt = false
	home.showPendingHooksPrompt()
	if got := home.confirmDialog.GetConfirmType(); got != ConfirmInstallKimiHooks {
		t.Fatalf("second prompt = %v, want Kimi hooks", got)
	}
}

func TestShouldPromptKimiHooks(t *testing.T) {
	for _, tt := range []struct {
		name      string
		installed bool
		decision  string
		want      bool
	}{
		{name: "new install", want: true},
		{name: "existing Agent Deck hooks", installed: true},
		{name: "durable decline", decision: "declined"},
		{name: "removed after acceptance is revocation", decision: "accepted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPromptKimiHooks(tt.installed, tt.decision); got != tt.want {
				t.Fatalf("shouldPromptKimiHooks(%v, %q) = %v, want %v", tt.installed, tt.decision, got, tt.want)
			}
		})
	}
}

func TestKimiHookConsentDecisionsAreDurable(t *testing.T) {
	for _, tt := range []struct {
		name      string
		accept    bool
		wantMeta  string
		wantHooks bool
	}{
		{name: "accept", accept: true, wantMeta: "accepted", wantHooks: true},
		{name: "decline", wantMeta: "declined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
			db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			previousDB := statedb.GetGlobal()
			statedb.SetGlobal(db)
			t.Cleanup(func() { statedb.SetGlobal(previousDB) })

			home := &Home{confirmDialog: NewConfirmDialog(), pendingKimiHooksPrompt: true}
			if tt.accept {
				home.confirmInstallKimiHooks()
				if home.hookWatcher != nil {
					t.Cleanup(home.hookWatcher.Stop)
				}
			} else {
				home.declineInstallKimiHooks()
			}
			gotMeta, err := db.GetMeta("kimi_hooks_prompted")
			if err != nil {
				t.Fatal(err)
			}
			if gotMeta != tt.wantMeta {
				t.Fatalf("Kimi consent = %q, want %q", gotMeta, tt.wantMeta)
			}
			if installed := session.CheckKimiHooksInstalled(session.GetKimiConfigDir()); installed != tt.wantHooks {
				t.Fatalf("Kimi hooks installed = %v, want %v", installed, tt.wantHooks)
			}
		})
	}
}

func TestKimiHookInstallFailureIsVisibleAndRetryable(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
	configDir := session.GetKimiConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("default_model = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })

	home := &Home{confirmDialog: NewConfirmDialog(), pendingKimiHooksPrompt: true}
	home.confirmInstallKimiHooks()
	if home.err == nil {
		t.Fatal("Kimi hook installation failure was not shown in the TUI")
	}
	view := home.confirmDialog.View()
	compactView := strings.ReplaceAll(strings.Join(strings.Fields(plain(view)), ""), "│", "")
	compactErr := strings.Join(strings.Fields(home.err.Error()), "")
	if !strings.Contains(compactView, compactErr) {
		t.Fatalf("reopened consent dialog hid the persistent installation failure %q:\n%s", home.err, view)
	}
	if !home.pendingKimiHooksPrompt {
		t.Fatal("Kimi hook installation failure cleared the pending consent prompt")
	}
	gotMeta, err := db.GetMeta("kimi_hooks_prompted")
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta != "" {
		t.Fatalf("Kimi consent recorded after failed installation: %q", gotMeta)
	}
	home.showPendingHooksPrompt()
	if !home.confirmDialog.IsVisible() || home.confirmDialog.GetConfirmType() != ConfirmInstallKimiHooks {
		t.Fatal("Kimi consent prompt did not reappear after failed installation")
	}

	if err := os.WriteFile(configPath, []byte("default_model = \"kimi-code/k3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home.confirmInstallKimiHooks()
	if home.hookWatcher != nil {
		t.Cleanup(home.hookWatcher.Stop)
	}
	gotMeta, err = db.GetMeta("kimi_hooks_prompted")
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta != "accepted" {
		t.Fatalf("Kimi consent after successful retry = %q, want accepted", gotMeta)
	}
}

func TestKimiHookPromptEscapeMeansLater(t *testing.T) {
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })

	home := NewHome()
	t.Cleanup(func() { cleanupKimiHooksHome(t, home) })
	home.initialLoading = false
	home.pendingKimiHooksPrompt = true
	home.showPendingHooksPrompt()
	home.handleConfirmDialogKey(tea.KeyMsg{Type: tea.KeyEsc})

	if home.confirmDialog.IsVisible() {
		t.Fatal("Esc did not dismiss the Kimi consent prompt")
	}
	model, _ := home.Update(loadSessionsMsg{})
	home = model.(*Home)
	if home.confirmDialog.IsVisible() {
		t.Fatal("Kimi consent prompt reopened on a session reload after Esc postponed it")
	}
	if !home.pendingKimiHooksPrompt {
		t.Fatal("Esc cleared the pending Kimi consent prompt instead of postponing it")
	}
	gotMeta, err := db.GetMeta("kimi_hooks_prompted")
	if err != nil {
		t.Fatal(err)
	}
	if gotMeta != "" {
		t.Fatalf("Esc recorded a durable Kimi consent decision: %q", gotMeta)
	}
}

func TestKimiHookPromptReloadDoesNotCoverAnotherModal(t *testing.T) {
	home := NewHome()
	t.Cleanup(func() { cleanupKimiHooksHome(t, home) })
	home.initialLoading = false
	home.pendingKimiHooksPrompt = true
	home.settingsPanel.Show()

	model, _ := home.Update(loadSessionsMsg{})
	home = model.(*Home)

	if !home.settingsPanel.IsVisible() {
		t.Fatal("test modal was unexpectedly closed by the session reload")
	}
	if home.confirmDialog.IsVisible() {
		t.Fatal("Kimi consent prompt opened over another modal on session reload")
	}
	if !home.pendingKimiHooksPrompt {
		t.Fatal("modal-visible reload cleared the pending Kimi consent prompt")
	}
}

func TestAcceptedKimiHooksRemovedLaterAreNotReinstalled(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
	t.Setenv("AGENTDECK_PROFILE", "kimi-revocation")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "kimi"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta("kimi_hooks_prompted", "accepted"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = true
	t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })
	stubLogMaintenance(t)

	home := NewHome()
	t.Cleanup(func() { cleanupKimiHooksHome(t, home) })
	configPath := filepath.Join(homeDir, ".kimi-code", "config.toml")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("startup reinstalled revoked Kimi hooks; stat err=%v", err)
	}
	if home.pendingKimiHooksPrompt {
		t.Fatal("revoked Kimi hooks prompted again despite durable acceptance")
	}
}
