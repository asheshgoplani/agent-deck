// Package session — telegram channel-plugin reliability helpers (issue #1138).
//
// PR #1136 closed the channel-owner case where the global antipattern
// (settings.telegram=true in the source profile) triggered scratch
// creation: scratch's settings.json now keeps the channel plugin
// enabled so `--channels` has a live MCP transport to wire onto.
//
// What was still wrong. Scratch creation was gated on three orthogonal
// signals — TG conductor present + non-channel-owning worker, explicit
// plugins, or the global antipattern — none of which fire for the
// recommended post-#941 topology (channel-owning conductor with the
// global flag DISABLED, no extra plugins). In that case
// NeedsWorkerScratchConfigDir returned false, no scratch was created,
// and the conductor depended entirely on the ambient settings.json
// carrying `telegram=true`. Any drift in the ambient (manual edit,
// Claude Code's `/plugin disable`, an out-of-band rewriter) silently
// disabled the channel transport. On the next restart there was no
// force-correct pass to heal it — only a passive `--channels` wiring
// directive that landed on nothing.
//
// What this file adds.
//   - needsScratchForTelegramChannelOwner: a fourth gate that always
//     fires for channel-owning sessions. The scratch becomes the single
//     point of truth for channel-plugin enablement and the heal point
//     on every spawn.
//   - VerifyTelegramChannelEnabled: pure-function check that returns a
//     structured result describing whether `--channels` will find a
//     live MCP server in the given config dir.
//   - EmitTelegramChannelDriftWarning: structured WARN-level log entry
//     with a stable code (`telegram_channel_plugin_drift`) so operators
//     and monitoring can detect the silent-failure topology.
//
// All three are wired into prepareWorkerScratchConfigDirForSpawn so the
// channel-owner is healed on every restart AND a loud warning fires if
// the effective state still doesn't enable the plugin (defense in depth).

package session

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// needsScratchForTelegramChannelOwner is the issue #1138 gate.
//
// A claude session whose Channels contains a `plugin:telegram@…` id
// ALWAYS needs a scratch CLAUDE_CONFIG_DIR so agent-deck owns the
// enablement of its own channel plugin. The scratch's settings.json is
// rewritten on every spawn (idempotent, force-correct), so any drift
// — manual edit, `/plugin disable`, external rewriter — is healed at
// the start of the next session. Without this gate, channel-owning
// conductors that have correctly DISABLED the global antipattern (per
// #941 guidance) end up trusting the ambient profile, which has no
// agent-deck-controlled invariant.
func needsScratchForTelegramChannelOwner(i *Instance) bool {
	if i == nil || i.Tool != "claude" {
		return false
	}
	return sessionHasTelegramChannel(i)
}

// TelegramChannelEnablementResult is the structured outcome of
// VerifyTelegramChannelEnabled. OK is the only consumer-relevant bit;
// Reason carries the human-readable diagnostic for log lines.
type TelegramChannelEnablementResult struct {
	// OK is true when the session has no telegram channel OR the
	// effective settings.json has telegram=true. Either case means
	// `--channels` will not silently land on a disabled plugin.
	OK bool

	// Reason describes the failure when OK=false. Empty when OK=true.
	// Stable English phrasing — operators grep for substrings.
	Reason string

	// EffectiveValue is the value found in settings.json
	// (true/false/absent). Carried so the warning can name the exact
	// drift variant the operator is observing.
	EffectiveValue string
}

// VerifyTelegramChannelEnabled inspects the given config dir's
// settings.json and reports whether a session whose `--channels`
// references `plugin:telegram@…` will find a live MCP transport.
//
// The check is conservative: a missing or unreadable settings.json is
// treated as "not enabled" (Reason fills in). This matches Claude
// Code's own behavior — absence equals disabled for channel plugins.
//
// Pure / read-only; safe to call from prepare path and from a runtime
// monitor (telegram-doctor CLI).
func VerifyTelegramChannelEnabled(configDir string, channels []string) TelegramChannelEnablementResult {
	// No telegram channel → vacuously OK; nothing to verify.
	hasTG := false
	for _, ch := range channels {
		if strings.HasPrefix(ch, telegramChannelPrefix) {
			hasTG = true
			break
		}
	}
	if !hasTG {
		return TelegramChannelEnablementResult{OK: true}
	}

	if configDir == "" {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "config dir is empty; cannot resolve effective settings.json",
			EffectiveValue: "no-config-dir",
		}
	}

	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return TelegramChannelEnablementResult{
				OK:             false,
				Reason:         "settings.json does not exist at " + settingsPath,
				EffectiveValue: "missing-file",
			}
		}
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "read settings.json failed: " + err.Error(),
			EffectiveValue: "read-error",
		}
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "settings.json is not valid JSON: " + err.Error(),
			EffectiveValue: "parse-error",
		}
	}

	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	if plugins == nil {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "settings.json has no enabledPlugins block; --channels has nothing to wire",
			EffectiveValue: "absent",
		}
	}
	raw, present := plugins[telegramPluginID]
	if !present {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "enabledPlugins is missing the " + telegramPluginID + " entry; --channels has nothing to wire",
			EffectiveValue: "absent",
		}
	}
	v, isBool := raw.(bool)
	if !isBool {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "enabledPlugins[" + telegramPluginID + "] is not a boolean",
			EffectiveValue: "non-bool",
		}
	}
	if !v {
		return TelegramChannelEnablementResult{
			OK:             false,
			Reason:         "enabledPlugins[" + telegramPluginID + "] = false; --channels cannot activate a disabled plugin",
			EffectiveValue: "false",
		}
	}
	return TelegramChannelEnablementResult{OK: true, EffectiveValue: "true"}
}

// EmitTelegramChannelDriftWarning logs a WARN-level structured entry
// when VerifyTelegramChannelEnabled returns OK=false for a session
// that owns a telegram channel. The code field is stable
// (`telegram_channel_plugin_drift`) so log-monitoring rules can
// trigger on it without parsing free-form text.
//
// Intentionally a no-op when result.OK is true — callers can invoke
// it unconditionally on the prepare path.
func EmitTelegramChannelDriftWarning(title, instanceID, configDir string, channels []string, result TelegramChannelEnablementResult) {
	if result.OK {
		return
	}
	sessionLog.Warn("telegram_channel_plugin_drift",
		slog.String("instance_id", instanceID),
		slog.String("title", title),
		slog.String("plugin_id", telegramPluginID),
		slog.String("config_dir", configDir),
		slog.String("channels", strings.Join(channels, ",")),
		slog.String("effective_value", result.EffectiveValue),
		slog.String("reason", result.Reason),
		slog.String("hint", "agent-deck telegram-doctor reports per-conductor health; the scratch CLAUDE_CONFIG_DIR is rewritten on every restart and should heal this drift on the next session restart."),
	)
}
