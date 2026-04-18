package session

import (
	"strings"
	"testing"
)

// Telegram-topology validator tests (fix v1.7.22, Closes #658).
//
// These tests enforce the Codex-approved three-root-cause fix:
//   A. Global enablement of telegram@claude-plugins-official in profile
//      settings.json → per-session poller leak.
//   B. Global enablement + --channels on the same session → plugin loaded
//      twice in one claude process → dueling bun pollers → Telegram 409.
//   C. wrapper-based TELEGRAM_STATE_DIR injection is silently unreliable on
//      the fresh-start path; env_file is the canonical mechanism.
//
// The validator is pure (no I/O) and takes the three inputs as struct fields.
// CLI layer is responsible for reading settings.json and feeding GlobalEnabled.

func TestTelegramValidator_GlobalDisabled_NoWarning(t *testing.T) {
	in := TelegramValidatorInput{
		GlobalEnabled: false,
		SessionChannels: []string{
			"plugin:telegram@claude-plugins-official",
			"plugin:slack@claude-plugins-official",
		},
		SessionWrapper: "ENV=foo {command}",
	}
	got := ValidateTelegramTopology(in)
	if len(got) != 0 {
		t.Fatalf("GlobalEnabled=false must produce zero warnings, got %d: %+v", len(got), got)
	}
}

func TestTelegramValidator_GlobalEnabled_OrdinarySession_WarnAntiPattern(t *testing.T) {
	in := TelegramValidatorInput{
		GlobalEnabled:   true,
		SessionChannels: nil, // ordinary child session, no telegram channel
		SessionWrapper:  "",
	}
	got := ValidateTelegramTopology(in)

	var antipattern *TelegramWarning
	for i := range got {
		if got[i].Code == "GLOBAL_ANTIPATTERN" {
			antipattern = &got[i]
			break
		}
	}
	if antipattern == nil {
		t.Fatalf("expected GLOBAL_ANTIPATTERN warning, got %+v", got)
	}
	if !strings.Contains(antipattern.Message, "enabledPlugins") {
		t.Errorf("GLOBAL_ANTIPATTERN message must reference enabledPlugins, got: %s", antipattern.Message)
	}
	if !strings.Contains(strings.ToLower(antipattern.Message), "telegram") {
		t.Errorf("GLOBAL_ANTIPATTERN message must reference telegram, got: %s", antipattern.Message)
	}
	// Must NOT emit DOUBLE_LOAD when session has no telegram channel.
	for _, w := range got {
		if w.Code == "DOUBLE_LOAD" {
			t.Errorf("ordinary session must not receive DOUBLE_LOAD warning, got: %+v", w)
		}
	}
}

func TestTelegramValidator_GlobalEnabled_ConductorSession_WarnDoubleLoad(t *testing.T) {
	in := TelegramValidatorInput{
		GlobalEnabled: true,
		SessionChannels: []string{
			"plugin:telegram@claude-plugins-official",
		},
		SessionWrapper: "",
	}
	got := ValidateTelegramTopology(in)

	codes := map[string]bool{}
	var doubleLoad *TelegramWarning
	for i := range got {
		codes[got[i].Code] = true
		if got[i].Code == "DOUBLE_LOAD" {
			doubleLoad = &got[i]
		}
	}
	if !codes["GLOBAL_ANTIPATTERN"] {
		t.Errorf("expected GLOBAL_ANTIPATTERN, got codes %+v", codes)
	}
	if doubleLoad == nil {
		t.Fatalf("expected DOUBLE_LOAD warning, got %+v", got)
	}
	msg := strings.ToLower(doubleLoad.Message)
	// message should explain the actual failure mode
	if !strings.Contains(msg, "twice") && !strings.Contains(msg, "duplicate") {
		t.Errorf("DOUBLE_LOAD message should explain plugin loaded twice, got: %s", doubleLoad.Message)
	}
	if !strings.Contains(msg, "409") && !strings.Contains(msg, "conflict") {
		t.Errorf("DOUBLE_LOAD message should reference 409/Conflict, got: %s", doubleLoad.Message)
	}
}

func TestTelegramValidator_WrapperStateDir_AntiPattern(t *testing.T) {
	in := TelegramValidatorInput{
		GlobalEnabled: false,
		SessionChannels: []string{
			"plugin:telegram@claude-plugins-official",
		},
		SessionWrapper: "TELEGRAM_STATE_DIR=/home/me/.claude/channels/telegram {command}",
	}
	got := ValidateTelegramTopology(in)

	var w *TelegramWarning
	for i := range got {
		if got[i].Code == "WRAPPER_DEPRECATED" {
			w = &got[i]
			break
		}
	}
	if w == nil {
		t.Fatalf("expected WRAPPER_DEPRECATED warning, got %+v", got)
	}
	if !strings.Contains(w.Message, "env_file") {
		t.Errorf("WRAPPER_DEPRECATED message must recommend env_file, got: %s", w.Message)
	}
}

// Sanity: wrapper with TELEGRAM_STATE_DIR on a session without telegram
// channel is NOT a v1.7.22-relevant antipattern (nothing to poll). Don't warn.
func TestTelegramValidator_WrapperStateDir_NoTelegramChannel_NoWarning(t *testing.T) {
	in := TelegramValidatorInput{
		GlobalEnabled:   false,
		SessionChannels: nil,
		SessionWrapper:  "TELEGRAM_STATE_DIR=/tmp/x {command}",
	}
	got := ValidateTelegramTopology(in)
	for _, w := range got {
		if w.Code == "WRAPPER_DEPRECATED" {
			t.Errorf("no telegram channel: must not emit WRAPPER_DEPRECATED, got %+v", w)
		}
	}
}
