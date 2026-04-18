package session

import "strings"

// Telegram-topology validator.
//
// Surfaces three anti-patterns that cause telegram poller leaks and 409
// Conflict lockouts across conductor hosts (fix v1.7.22, issue #658):
//
//	GLOBAL_ANTIPATTERN — enabledPlugins."telegram@claude-plugins-official"=true
//	                     in a profile settings.json makes every claude
//	                     session load the plugin.
//	DOUBLE_LOAD        — global + --channels on the same session makes the
//	                     plugin load twice in one process → 409.
//	WRAPPER_DEPRECATED — TELEGRAM_STATE_DIR injected via session wrapper is
//	                     unreliable on the fresh-start path. Use
//	                     [conductors.<name>.claude].env_file instead.
//
// Pure and side-effect-free. CLI layer owns I/O (reading settings.json) and
// presentation (formatting warnings on stderr).

// telegramChannelPrefix matches channel ids like
// "plugin:telegram@claude-plugins-official" or any other
// "plugin:telegram@<owner>/<repo>" variant. We match by prefix so forks and
// repo renames still trigger the guard.
const telegramChannelPrefix = "plugin:telegram@"

// TelegramValidatorInput captures the three signals the validator inspects.
type TelegramValidatorInput struct {
	// GlobalEnabled is the value of
	// enabledPlugins."telegram@claude-plugins-official" in the relevant
	// profile's settings.json, or false if that file or key is absent.
	GlobalEnabled bool

	// SessionChannels is the list of channels the session is launched with
	// (Instance.Channels). Empty for ordinary child sessions.
	SessionChannels []string

	// SessionWrapper is the wrapper template for the session (may be empty).
	SessionWrapper string
}

// TelegramWarning is one emission from the validator.
type TelegramWarning struct {
	Code    string // GLOBAL_ANTIPATTERN | DOUBLE_LOAD | WRAPPER_DEPRECATED
	Message string
}

// ValidateTelegramTopology returns zero or more warnings for the given
// session configuration. Ordering is stable: GLOBAL_ANTIPATTERN,
// DOUBLE_LOAD, WRAPPER_DEPRECATED.
func ValidateTelegramTopology(in TelegramValidatorInput) []TelegramWarning {
	hasTelegramChannel := false
	for _, ch := range in.SessionChannels {
		if strings.HasPrefix(ch, telegramChannelPrefix) {
			hasTelegramChannel = true
			break
		}
	}

	var out []TelegramWarning

	if in.GlobalEnabled {
		out = append(out, TelegramWarning{
			Code:    "GLOBAL_ANTIPATTERN",
			Message: `enabledPlugins."telegram@claude-plugins-official"=true in the profile settings.json is an anti-pattern for conductor hosts. Every claude session launched under this profile will start a telegram poller — including child agents spawned by the conductor. Disable the global flag and activate telegram per-session via --channels instead.`,
		})
		if hasTelegramChannel {
			out = append(out, TelegramWarning{
				Code:    "DOUBLE_LOAD",
				Message: `Global telegram enablement AND --channels telegram@... on the same session: the plugin is loaded twice in one claude process. Two bun pollers race for the same bot token and Telegram rejects one with 409 Conflict. The supported topology is one channel-owning session per bot token with the global flag disabled (see skills/agent-deck SKILL.md "Telegram conductor topology").`,
			})
		}
	}

	if hasTelegramChannel && strings.Contains(in.SessionWrapper, "TELEGRAM_STATE_DIR=") {
		out = append(out, TelegramWarning{
			Code:    "WRAPPER_DEPRECATED",
			Message: `The session wrapper injects TELEGRAM_STATE_DIR. This path is deprecated because bash -c argv splitting makes it unreliable on the fresh-start path (claude without --resume). Inject the variable via [conductors.<name>.claude].env_file in ~/.agent-deck/config.toml — it is sourced deterministically on both fresh and resume spawns.`,
		})
	}

	return out
}
