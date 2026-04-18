package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	hotkeyQuit            = "quit"
	hotkeyNewSession      = "new_session"
	hotkeyQuickCreate     = "quick_create"
	hotkeyRename          = "rename"
	hotkeyRestart         = "restart"
	hotkeyRestartFresh    = "restart_fresh"
	hotkeyDelete          = "delete"
	hotkeyCloseSession    = "close_session"
	hotkeyUndoDelete      = "undo_delete"
	hotkeyMoveToGroup     = "move_to_group"
	hotkeyMCPManager      = "mcp_manager"
	hotkeySkillsManager   = "skills_manager"
	hotkeyTogglePreview   = "toggle_preview"
	hotkeyMarkUnread      = "mark_unread"
	hotkeyToggleYolo      = "toggle_yolo"
	hotkeyQuickFork       = "quick_fork"
	hotkeyForkWithOptions = "fork_with_options"
	hotkeyCopyOutput      = "copy_output"
	hotkeySendOutput      = "send_output"
	hotkeyExecShell       = "exec_shell"
	hotkeyEditNotes       = "edit_notes"
	hotkeyEditPaths       = "edit_paths"
	hotkeyWorktreeFinish  = "worktree_finish"
	hotkeyCreateGroup     = "create_group"
	hotkeySearch          = "search"
	hotkeyHelp            = "help"
	hotkeySettings        = "settings"
	hotkeyImport          = "import"
	hotkeyReload          = "reload"
	hotkeyDetach          = "detach"
	hotkeyWatcherPanel    = "watcher_panel"
)

var hotkeyActionOrder = []string{
	hotkeyQuit,
	hotkeyNewSession,
	hotkeyQuickCreate,
	hotkeyRename,
	hotkeyRestart,
	hotkeyRestartFresh,
	hotkeyDelete,
	hotkeyCloseSession,
	hotkeyUndoDelete,
	hotkeyMoveToGroup,
	hotkeyMCPManager,
	hotkeySkillsManager,
	hotkeyTogglePreview,
	hotkeyMarkUnread,
	hotkeyToggleYolo,
	hotkeyQuickFork,
	hotkeyForkWithOptions,
	hotkeyCopyOutput,
	hotkeySendOutput,
	hotkeyExecShell,
	hotkeyEditNotes,
	hotkeyEditPaths,
	hotkeyWorktreeFinish,
	hotkeyCreateGroup,
	hotkeySearch,
	hotkeyHelp,
	hotkeySettings,
	hotkeyImport,
	hotkeyReload,
	hotkeyDetach,
	hotkeyWatcherPanel,
}

var defaultHotkeyBindings = map[string]string{
	hotkeyQuit:            "q",
	hotkeyNewSession:      "n",
	hotkeyQuickCreate:     "N",
	hotkeyRename:          "r",
	hotkeyRestart:         "R",
	hotkeyRestartFresh:    "T",
	hotkeyDelete:          "d",
	hotkeyCloseSession:    "D",
	hotkeyUndoDelete:      "ctrl+z",
	hotkeyMoveToGroup:     "M",
	hotkeyMCPManager:      "m",
	hotkeySkillsManager:   "s",
	hotkeyTogglePreview:   "v",
	hotkeyMarkUnread:      "u",
	hotkeyToggleYolo:      "y",
	hotkeyQuickFork:       "f",
	hotkeyForkWithOptions: "F",
	hotkeyCopyOutput:      "c",
	hotkeySendOutput:      "x",
	hotkeyExecShell:       "E",
	hotkeyEditNotes:       "e",
	hotkeyEditPaths:       "p",
	hotkeyWorktreeFinish:  "W",
	hotkeyCreateGroup:     "g",
	hotkeySearch:          "/",
	hotkeyHelp:            "?",
	hotkeySettings:        "S",
	hotkeyImport:          "i",
	hotkeyReload:          "ctrl+r",
	hotkeyDetach:          "ctrl+q",
	hotkeyWatcherPanel:    "w",
}

var hotkeyActionDefaultTriggers = map[string][]string{
	hotkeyQuit:            {"q", "ctrl+c"},
	hotkeyForkWithOptions: {"F", "shift+f"},
	hotkeyMoveToGroup:     {"M", "shift+m"},
	hotkeyWorktreeFinish:  {"W", "shift+w"},
}

// renamedHotkeys maps old action names to new names for backward compatibility.
var renamedHotkeys = map[string]string{
	"toggle_gemini_yolo": hotkeyToggleYolo,
}

func resolveHotkeys(overrides map[string]string) map[string]string {
	bindings := make(map[string]string, len(defaultHotkeyBindings))
	for action, key := range defaultHotkeyBindings {
		bindings[action] = key
	}

	overrideActions := make([]string, 0, len(overrides))
	for action := range overrides {
		overrideActions = append(overrideActions, action)
	}
	sort.Strings(overrideActions)

	canonicalOverrides := make(map[string]string, len(overrides))
	for _, action := range overrideActions {
		key := overrides[action]
		normalizedAction := strings.TrimSpace(strings.ToLower(action))
		normalizedKey := strings.TrimSpace(key)

		if _, ok := defaultHotkeyBindings[normalizedAction]; ok {
			canonicalOverrides[normalizedAction] = normalizedKey
		}
	}
	for _, action := range overrideActions {
		key := overrides[action]
		normalizedAction := strings.TrimSpace(strings.ToLower(action))
		newName, ok := renamedHotkeys[normalizedAction]
		if !ok {
			continue
		}
		if _, exists := canonicalOverrides[newName]; exists {
			continue
		}
		canonicalOverrides[newName] = strings.TrimSpace(key)
	}

	for action, key := range canonicalOverrides {
		if key == "" {
			delete(bindings, action)
			continue
		}
		bindings[action] = key
	}

	return bindings
}

func buildHotkeyLookup(bindings map[string]string) (map[string]string, map[string]bool) {
	keyToCanonical := make(map[string]string, len(bindings))
	blockedCanonical := make(map[string]bool)

	for _, action := range hotkeyActionOrder {
		canonical := defaultHotkeyBindings[action]
		bound := strings.TrimSpace(bindings[action])
		defaultTriggers := defaultTriggersForAction(action)
		if bound == "" {
			for _, trigger := range defaultTriggers {
				blockedCanonical[trigger] = true
			}
			continue
		}
		if bound != canonical {
			for _, trigger := range defaultTriggers {
				blockedCanonical[trigger] = true
			}
		}
		for _, alias := range hotkeyAliases(bound) {
			if _, exists := keyToCanonical[alias]; !exists {
				keyToCanonical[alias] = canonical
			}
		}
	}

	return keyToCanonical, blockedCanonical
}

func defaultTriggersForAction(action string) []string {
	if triggers, ok := hotkeyActionDefaultTriggers[action]; ok {
		return triggers
	}
	return hotkeyAliases(defaultHotkeyBindings[action])
}

func hotkeyAliases(key string) []string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil
	}

	aliases := []string{trimmed}
	seen := map[string]bool{trimmed: true}
	add := func(alias string) {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			return
		}
		seen[alias] = true
		aliases = append(aliases, alias)
	}

	if shiftAlias := shiftedAliasFor(trimmed); shiftAlias != "" {
		add(shiftAlias)
	}
	if unshiftedAlias := unshiftedAliasFor(trimmed); unshiftedAlias != "" {
		add(unshiftedAlias)
	}

	return aliases
}

func shiftedAliasFor(key string) string {
	runes := []rune(key)
	if len(runes) != 1 {
		return ""
	}

	r := runes[0]
	if unicode.IsUpper(r) {
		return "shift+" + strings.ToLower(string(r))
	}

	switch r {
	case '!':
		return "shift+1"
	case '@':
		return "shift+2"
	case '#':
		return "shift+3"
	case '$':
		return "shift+4"
	case '%':
		return "shift+5"
	case '^':
		return "shift+6"
	case '&':
		return "shift+7"
	case '*':
		return "shift+8"
	case '(':
		return "shift+9"
	case ')':
		return "shift+0"
	}

	return ""
}

func unshiftedAliasFor(key string) string {
	lower := strings.ToLower(strings.TrimSpace(key))
	if !strings.HasPrefix(lower, "shift+") {
		return ""
	}

	base := strings.TrimSpace(lower[len("shift+"):])
	runes := []rune(base)
	if len(runes) != 1 {
		return ""
	}

	r := runes[0]
	if unicode.IsLetter(r) {
		return strings.ToUpper(string(r))
	}

	switch r {
	case '1':
		return "!"
	case '2':
		return "@"
	case '3':
		return "#"
	case '4':
		return "$"
	case '5':
		return "%"
	case '6':
		return "^"
	case '7':
		return "&"
	case '8':
		return "*"
	case '9':
		return "("
	case '0':
		return ")"
	}

	return ""
}

func actionHotkey(bindings map[string]string, action string) string {
	if bindings == nil {
		return ""
	}
	return strings.TrimSpace(bindings[action])
}

func joinHotkeyLabels(keys ...string) string {
	filtered := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, "/")
}

// DetachByteFromBinding converts a hotkey binding string (e.g. "ctrl+q") to the
// corresponding ASCII byte used by the PTY attach loop. Returns 0x11 (Ctrl+Q) as
// the default when the binding cannot be mapped.
func DetachByteFromBinding(binding string) byte {
	binding = strings.ToLower(strings.TrimSpace(binding))
	if !strings.HasPrefix(binding, "ctrl+") {
		return 17 // default Ctrl+Q
	}
	ch := binding[len("ctrl+"):]
	if len(ch) == 1 && ch[0] >= 'a' && ch[0] <= 'z' {
		return ch[0] - 'a' + 1
	}
	switch ch {
	case "\\":
		return 0x1C
	case "]":
		return 0x1D
	case "^":
		return 0x1E
	case "_":
		return 0x1F
	}
	return 17 // default Ctrl+Q
}

// DetachByteLabel returns a human-readable label for a detach byte (e.g. "Ctrl+Q").
func DetachByteLabel(b byte) string {
	if b >= 1 && b <= 26 {
		return fmt.Sprintf("Ctrl+%c", 'A'+b-1)
	}
	switch b {
	case 0x1C:
		return "Ctrl+\\"
	case 0x1D:
		return "Ctrl+]"
	case 0x1E:
		return "Ctrl+^"
	case 0x1F:
		return "Ctrl+_"
	}
	return "Ctrl+Q"
}

// ResolvedDetachByte returns the detach byte for the current hotkey configuration.
func ResolvedDetachByte(overrides map[string]string) byte {
	bindings := resolveHotkeys(overrides)
	key := actionHotkey(bindings, hotkeyDetach)
	if key == "" {
		return 17 // default Ctrl+Q
	}
	return DetachByteFromBinding(key)
}
