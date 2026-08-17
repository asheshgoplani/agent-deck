package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestHermesDetectionDoesNotWriteConfigBeforeConsent(t *testing.T) {
	tests := []struct {
		name    string
		initial []byte
	}{
		{name: "missing config"},
		{name: "existing config", initial: []byte("model: test-model\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			t.Setenv("AGENTDECK_PROFILE", "hermes-consent-"+tt.name)
			t.Setenv("AGENTDECK_HERMES_HOOK_VOCABULARY", "legacy")

			binDir := t.TempDir()
			hermesBin := filepath.Join(binDir, "hermes")
			if err := os.WriteFile(hermesBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("write fake Hermes binary: %v", err)
			}
			t.Setenv("PATH", binDir)

			configPath := filepath.Join(homeDir, ".hermes", "config.yaml")
			if tt.initial != nil {
				if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
					t.Fatalf("mkdir Hermes config dir: %v", err)
				}
				if err := os.WriteFile(configPath, tt.initial, 0o600); err != nil {
					t.Fatalf("seed Hermes config: %v", err)
				}
			}

			previousWorkers := homeBackgroundWorkersEnabled
			homeBackgroundWorkersEnabled = true
			t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })
			previousDB := statedb.GetGlobal()
			statedb.SetGlobal(nil)
			t.Cleanup(func() { statedb.SetGlobal(previousDB) })

			home := NewHome()
			t.Cleanup(func() {
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
			})
			if !home.pendingHermesHooksPrompt {
				t.Fatal("Hermes detection did not queue a Hermes-specific consent prompt")
			}

			got, err := os.ReadFile(configPath)
			if tt.initial == nil {
				if !os.IsNotExist(err) {
					t.Fatalf("Hermes detection wrote %s before consent; read err=%v, content=%q", configPath, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("read Hermes config after detection: %v", err)
			}
			if string(got) != string(tt.initial) {
				t.Fatalf("Hermes detection mutated config before consent:\n got %q\nwant %q", got, tt.initial)
			}
			if session.CheckHermesHooksInstalled(filepath.Dir(configPath)) {
				t.Fatal("Hermes hooks installed before consent")
			}
		})
	}
}

func TestHermesHooksDialogDisclosesMutationAndDefaultsToSkip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ".hermes", "config.yaml")
	t.Setenv("AGENTDECK_HERMES_HOOK_VOCABULARY", "extended")
	dialog := NewConfirmDialog()
	dialog.ShowInstallHermesHooks(configPath, session.HermesHookEventsForInstall())

	if dialog.GetConfirmType() != ConfirmInstallHermesHooks {
		t.Fatalf("confirm type = %v, want ConfirmInstallHermesHooks", dialog.GetConfirmType())
	}
	if dialog.GetFocusedButton() != 1 {
		t.Fatalf("focused button = %d, want 1 (Skip)", dialog.GetFocusedButton())
	}
	view := dialog.View()
	if dialog.GetTargetID() != configPath {
		t.Errorf("dialog config path = %q, want %q", dialog.GetTargetID(), configPath)
	}
	if !strings.Contains(view, "Config:") {
		t.Errorf("dialog does not label the config path:\n%s", view)
	}
	for _, disclosed := range []string{
		"Hermes Agent Hooks",
		"agent-deck hook-handler",
		"user", "permissions", "JSON", "stdin",
		"pre_llm_call", "post_llm_call", "pre_api_request", "post_api_request",
		"pre_tool_call", "post_tool_call", "on_session_start", "on_session_end", "on_session_finalize",
	} {
		if !strings.Contains(view, disclosed) {
			t.Errorf("dialog does not disclose %q:\n%s", disclosed, view)
		}
	}
}

func TestPendingHookPromptsAreSequencedClaudeThenHermes(t *testing.T) {
	home := &Home{
		confirmDialog:            NewConfirmDialog(),
		pendingHooksPrompt:       true,
		pendingHermesHooksPrompt: true,
	}
	home.showPendingHooksPrompt()
	if got := home.confirmDialog.GetConfirmType(); got != ConfirmInstallHooks {
		t.Fatalf("first prompt = %v, want Claude hooks", got)
	}
	home.pendingHooksPrompt = false
	home.showPendingHooksPrompt()
	if got := home.confirmDialog.GetConfirmType(); got != ConfirmInstallHermesHooks {
		t.Fatalf("second prompt = %v, want Hermes hooks", got)
	}
}

func TestShouldPromptHermesHooks(t *testing.T) {
	tests := []struct {
		name      string
		installed bool
		decision  string
		want      bool
	}{
		{name: "new install", want: true},
		{name: "existing Agent Deck hooks", installed: true, want: false},
		{name: "durable decline", decision: "declined", want: false},
		{name: "removed after acceptance is revocation", decision: "accepted", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPromptHermesHooks(tt.installed, tt.decision); got != tt.want {
				t.Fatalf("shouldPromptHermesHooks(%v, %q) = %v, want %v", tt.installed, tt.decision, got, tt.want)
			}
		})
	}
}

func TestHermesHookConsentDecisionsAreDurable(t *testing.T) {
	tests := []struct {
		name      string
		accept    bool
		wantMeta  string
		wantHooks bool
	}{
		{name: "accept", accept: true, wantMeta: "accepted", wantHooks: true},
		{name: "decline", wantMeta: "declined", wantHooks: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			t.Setenv("AGENTDECK_HERMES_HOOK_VOCABULARY", "legacy")
			db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatalf("open state db: %v", err)
			}
			if err := db.Migrate(); err != nil {
				t.Fatalf("migrate state db: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			previousDB := statedb.GetGlobal()
			statedb.SetGlobal(db)
			t.Cleanup(func() { statedb.SetGlobal(previousDB) })

			home := &Home{confirmDialog: NewConfirmDialog(), pendingHermesHooksPrompt: true}
			if tt.accept {
				home.confirmInstallHermesHooks()
				if home.hookWatcher != nil {
					t.Cleanup(home.hookWatcher.Stop)
				}
			} else {
				home.declineInstallHermesHooks()
			}

			gotMeta, err := db.GetMeta("hermes_hooks_prompted")
			if err != nil {
				t.Fatalf("read Hermes consent: %v", err)
			}
			if gotMeta != tt.wantMeta {
				t.Fatalf("Hermes consent = %q, want %q", gotMeta, tt.wantMeta)
			}
			installed := session.CheckHermesHooksInstalled(session.GetHermesConfigDir())
			if installed != tt.wantHooks {
				t.Fatalf("Hermes hooks installed = %v, want %v", installed, tt.wantHooks)
			}
		})
	}
}

func TestAcceptedHermesHooksRemovedLaterAreNotReinstalled(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("AGENTDECK_PROFILE", "hermes-revocation")
	t.Setenv("AGENTDECK_HERMES_HOOK_VOCABULARY", "legacy")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "hermes"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake Hermes binary: %v", err)
	}
	t.Setenv("PATH", binDir)

	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate state db: %v", err)
	}
	if err := db.SetMeta("hermes_hooks_prompted", "accepted"); err != nil {
		t.Fatalf("record accepted consent: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = true
	t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })

	home := NewHome()
	t.Cleanup(func() {
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
	})

	configPath := filepath.Join(homeDir, ".hermes", "config.yaml")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("startup reinstalled revoked Hermes hooks at %s; stat err=%v", configPath, err)
	}
	if home.pendingHermesHooksPrompt {
		t.Fatal("revoked Hermes hooks prompted again despite a durable prior decision")
	}
}
