package ui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	hotkeyQuit             = "quit"
	hotkeyNewSession       = "new_session"
	hotkeyQuickCreate      = "quick_create"
	hotkeyRename           = "rename"
	hotkeyRestart          = "restart"
	hotkeyRestartFresh     = "restart_fresh"
	hotkeyDelete           = "delete"
	hotkeyCloseSession     = "close_session"
	hotkeyArchiveSession   = "archive_session"
	hotkeyUnarchiveSession = "unarchive_session"
	hotkeyViewArchived     = "view_archived"
	hotkeyUndoDelete       = "undo_delete"
	hotkeyMoveToGroup      = "move_to_group"
	hotkeyMCPManager       = "mcp_manager"
	hotkeyPluginManager    = "plugin_manager"
	hotkeySkillsManager    = "skills_manager"
	hotkeyTogglePreview    = "toggle_preview"
	hotkeyCycleGroupView   = "cycle_group_view"
	hotkeyCycleTimeFilter  = "cycle_time_filter"
	hotkeyMarkUnread       = "mark_unread"
	hotkeyQuickApprove     = "quick_approve"
	hotkeyPromptSession    = "prompt_session" // #1410: prompt the highlighted session without attaching
	hotkeyToggleYolo       = "toggle_yolo"
	hotkeyQuickFork        = "quick_fork"
	hotkeyForkWithOptions  = "fork_with_options"
	hotkeyCopyOutput       = "copy_output"
	hotkeyCopyPane         = "copy_pane"
	// hotkeyCopyInfo copies the preview pane's Repo / Path / Branch block
	// (#791). It shipped hardcoded on "C"; that key now opens the context
	// inspector, so this moved to "B" and became rebindable like everything
	// else. Users who want the old key back set [hotkeys].copy_info = "C" and
	// [hotkeys].context_inspector to something else.
	hotkeyCopyInfo = "copy_info"
	// hotkeyContextInspector opens the full-context overlay: everything the
	// harness is being sent, ranked by what it costs, with a lever per item.
	hotkeyContextInspector = "context_inspector"
	hotkeySendOutput       = "send_output"
	hotkeyExecShell        = "exec_shell"
	hotkeyOpenShellHere    = "open_shell_here"
	hotkeyEditNotes        = "edit_notes"
	hotkeyEditPaths        = "edit_paths"
	hotkeyEditSession      = "edit_session"
	hotkeyWorktreeSetup    = "worktree_setup"
	hotkeyWorktreeFinish   = "worktree_finish"
	hotkeyCreateGroup      = "create_group"
	hotkeySearch           = "search"
	hotkeyHelp             = "help"
	hotkeySettings         = "settings"
	hotkeyImport           = "import"
	hotkeyReload           = "reload"
	hotkeyRestartDeck      = "restart_deck"   // in-place TUI restart after an update landed on disk (restart.go)
	hotkeyInstallUpdate    = "install_update" // run `agent-deck update` from the TUI (update_install.go)
	hotkeyDetach           = "detach"
	hotkeyWatcherPanel     = "watcher_panel"
	hotkeyDeadLetters      = "dead_letters"
	// hotkeyAgentsPanel opens the Agents tab.
	//
	// The design mockup asks for "a". Every plain letter that reads as
	// "agents" is already taken by a shipped binding — "a" is quick_approve,
	// "A" is archive_session, and "G" is the hardcoded global-search case in
	// updateInner — and rebinding one of those out from under existing muscle
	// memory is not this feature's call to make. "alt+a" keeps the mockup's
	// mnemonic on a chord that nothing else claims. A user who would rather
	// have the bare key can set [hotkeys].agents_panel = "a" and move
	// quick_approve.
	hotkeyAgentsPanel = "agents_panel"
	// Session switcher. While attached it is intercepted in the tmux attach
	// loop (see internal/tmux/pty.go AttachOptions); on the home screen it is
	// dispatched like any other hotkey. Must resolve to a "ctrl+<letter>" chord.
	//
	// Disabled by default (see defaultDisabledHotkeys): intercepting it while
	// attached steals the control byte from the attached program, and the
	// suggested Ctrl+S collides with Claude Code (stash prompt) and XOFF
	// flow-control. Users opt in by binding [hotkeys].switch_session.
	hotkeySwitchSession = "switch_session" // canonical "ctrl+s" (opt-in)
	// hotkeyAltSession is the vim-style alternate-session toggle (#2058): one
	// key that swaps between the current session and the one you were on
	// immediately before, the way Ctrl+^ swaps vim's alternate buffer. Backed
	// by MRUHistory.Alternate (internal/session/mru.go).
	hotkeyAltSession = "alt_session"
	// hotkeyMRUBack / hotkeyMRUForward walk back/forward through recently
	// visited sessions (#2058), MRU-ordered via the persisted last_accessed
	// column (MRUHistory.WalkBack/WalkForward). A burst of either key holds
	// the walk order stable rather than reshuffling after each hop.
	hotkeyMRUBack    = "mru_back"
	hotkeyMRUForward = "mru_forward"
	// Scrollback pager. While attached to a session from the deck (Enter), the
	// deck owns the viewport so tmux's own copy-mode/scrollback is unreachable
	// (#1491). This trigger, intercepted in the attach loop, opens an in-view
	// scrollable pager rendered from the pane's history. Unlike other hotkeys
	// its value is not a home-screen key: it is "pageup" (default), a
	// "ctrl+<letter>" chord, or "" (disabled). Resolved by
	// ResolvedScrollbackTrigger, kept out of the home-screen dispatch maps.
	hotkeyScrollback = "scrollback"
)

// defaultScrollbackTrigger is the out-of-the-box scrollback trigger: a bare
// PageUp, exactly the key the #1491 reporter pressed expecting to scroll.
const defaultScrollbackTrigger = "pageup"

var hotkeyActionOrder = []string{
	hotkeyQuit,
	hotkeyNewSession,
	hotkeyQuickCreate,
	hotkeyRename,
	hotkeyRestart,
	hotkeyRestartFresh,
	hotkeyDelete,
	hotkeyCloseSession,
	hotkeyArchiveSession,
	hotkeyUnarchiveSession,
	hotkeyViewArchived,
	hotkeyUndoDelete,
	hotkeyMoveToGroup,
	hotkeyMCPManager,
	hotkeyPluginManager,
	hotkeySkillsManager,
	hotkeyTogglePreview,
	hotkeyCycleGroupView,
	hotkeyCycleTimeFilter,
	hotkeyMarkUnread,
	hotkeyQuickApprove,
	hotkeyPromptSession,
	hotkeyToggleYolo,
	hotkeyQuickFork,
	hotkeyForkWithOptions,
	hotkeyCopyOutput,
	hotkeyCopyPane,
	hotkeyCopyInfo,
	hotkeyContextInspector,
	hotkeySendOutput,
	hotkeyExecShell,
	hotkeyOpenShellHere,
	hotkeyEditNotes,
	hotkeyEditPaths,
	hotkeyEditSession,
	hotkeyWorktreeSetup,
	hotkeyWorktreeFinish,
	hotkeyCreateGroup,
	hotkeySearch,
	hotkeyHelp,
	hotkeySettings,
	hotkeyImport,
	hotkeyReload,
	hotkeyRestartDeck,
	hotkeyInstallUpdate,
	hotkeyDetach,
	hotkeyWatcherPanel,
	hotkeyDeadLetters,
	hotkeyAgentsPanel,
	hotkeySwitchSession,
	hotkeyAltSession,
	hotkeyMRUBack,
	hotkeyMRUForward,
}

var defaultHotkeyBindings = map[string]string{
	hotkeyQuit:             "q",
	hotkeyNewSession:       "n",
	hotkeyQuickCreate:      "N",
	hotkeyRename:           "r",
	hotkeyRestart:          "R",
	hotkeyRestartFresh:     "T",
	hotkeyDelete:           "d",
	hotkeyCloseSession:     "D",
	hotkeyArchiveSession:   "A",
	hotkeyUnarchiveSession: "shift+u",
	hotkeyViewArchived:     "^",
	hotkeyUndoDelete:       "ctrl+z",
	hotkeyMoveToGroup:      "M",
	hotkeyMCPManager:       "m",
	hotkeyPluginManager:    "L",
	hotkeySkillsManager:    "s",
	hotkeyTogglePreview:    "v",
	hotkeyCycleGroupView:   "t",
	hotkeyCycleTimeFilter:  "*",
	hotkeyMarkUnread:       "u",
	hotkeyQuickApprove:     "a",
	hotkeyPromptSession:    "o",
	hotkeyToggleYolo:       "y",
	hotkeyQuickFork:        "f",
	hotkeyForkWithOptions:  "F",
	hotkeyCopyOutput:       "c",
	hotkeyCopyPane:         "V",
	hotkeyCopyInfo:         "B",
	hotkeyContextInspector: "C",
	hotkeySendOutput:       "x",
	hotkeyExecShell:        "E",
	hotkeyOpenShellHere:    "H",
	hotkeyEditNotes:        "e",
	hotkeyEditPaths:        "p",
	hotkeyEditSession:      "P",
	hotkeyWorktreeSetup:    "b",
	hotkeyWorktreeFinish:   "W",
	hotkeyCreateGroup:      "g",
	hotkeySearch:           "/",
	hotkeyHelp:             "?",
	hotkeySettings:         "S",
	hotkeyImport:           "i",
	hotkeyReload:           "ctrl+r",
	hotkeyRestartDeck:      "ctrl+t",
	hotkeyInstallUpdate:    "ctrl+y",
	hotkeyDetach:           "ctrl+q",
	hotkeyWatcherPanel:     "w",
	hotkeyDeadLetters:      "alt+d",
	hotkeyAgentsPanel:      "alt+a",
	hotkeySwitchSession:    "ctrl+s",
	hotkeyAltSession:       "`",
	hotkeyMRUBack:          "alt+left",
	hotkeyMRUForward:       "alt+right",
}

var hotkeyActionDefaultTriggers = map[string][]string{
	hotkeyQuit:            {"q", "ctrl+c"},
	hotkeyForkWithOptions: {"F", "shift+f"},
	hotkeyMoveToGroup:     {"M", "shift+m"},
	hotkeyWorktreeFinish:  {"W", "shift+w"},
	hotkeyEditSession:     {"P", "shift+p"},
}

// renamedHotkeys maps old action names to new names for backward compatibility.
var renamedHotkeys = map[string]string{
	"toggle_gemini_yolo": hotkeyToggleYolo,
}

// defaultDisabledHotkeys are actions that keep a canonical key in
// defaultHotkeyBindings (so the home-screen dispatch case and help/status
// labels resolve) but ship UNBOUND: resolveHotkeys drops them unless the user
// binds them explicitly. switch_session is opt-in because enabling it
// intercepts a control byte in the attach loop before the attached program
// sees it — the suggested Ctrl+S collides with Claude Code's stash-prompt and
// terminal XOFF flow-control, and no control byte is safe to steal from every
// attached tool.
var defaultDisabledHotkeys = map[string]bool{
	hotkeySwitchSession: true,
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

	// Opt-in actions ship unbound: drop them unless the user set them
	// explicitly. The canonical default stays in defaultHotkeyBindings so the
	// dispatch case and labels resolve once a user binds it.
	for action := range defaultDisabledHotkeys {
		if _, overridden := canonicalOverrides[action]; !overridden {
			delete(bindings, action)
		}
	}

	return bindings
}

// buildHotkeyLookup resolves in two passes so a key the user wrote into
// [hotkeys] always belongs to that action. Pass one registers every action's
// bound key with its shift/unshift spellings; pass two adds the layout twins,
// but only onto keys nobody has claimed. A derived twin therefore never
// shadows an explicit binding and never blocks a canonical key: quick_fork =
// "т" keeps "т" even though it is also the ЙЦУКЕН twin of new_session's "n".
func buildHotkeyLookup(bindings map[string]string) (map[string]string, map[string]bool) {
	keyToCanonical := make(map[string]string, len(bindings))
	blockedCanonical := make(map[string]bool)

	// Actions that ended up with a binding, in hotkeyActionOrder, for pass two.
	var active []string

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
		for _, alias := range explicitHotkeyAliases(bound) {
			if _, exists := keyToCanonical[alias]; !exists {
				keyToCanonical[alias] = canonical
			}
		}
		active = append(active, action)
	}

	for _, action := range active {
		canonical := defaultHotkeyBindings[action]
		for _, alias := range layoutHotkeyAliases(bindings[action]) {
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

// hotkeyAliases lists every spelling that reaches a binding: the explicit
// shift/unshift forms first, then the layout twins derived from them. Callers
// that need the two tiers apart (buildHotkeyLookup) use the halves directly.
func hotkeyAliases(key string) []string {
	return append(explicitHotkeyAliases(key), layoutHotkeyAliases(key)...)
}

// explicitHotkeyAliases returns the binding itself plus its shifted and
// unshifted spellings ("F" <-> "shift+f", "!" <-> "shift+1"). These are the
// forms a user could have written, so they take part in first-wins resolution.
func explicitHotkeyAliases(key string) []string {
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

// layoutHotkeyAliases returns the layout twins of a binding's explicit
// spellings, so "shift+u" picks up the twin of its "U" form too. Twins are
// derived, never written by the user, so they are registered only after every
// explicit binding has claimed its key.
func layoutHotkeyAliases(key string) []string {
	explicit := explicitHotkeyAliases(key)
	var twins []string
	seen := make(map[string]bool, len(explicit))
	for _, alias := range explicit {
		seen[alias] = true
	}
	for _, alias := range explicit {
		twin := layoutAliasFor(alias)
		if twin == "" || seen[twin] {
			continue
		}
		seen[twin] = true
		twins = append(twins, twin)
	}
	return twins
}

// jcukenLetters maps each QWERTY letter key to the letter the same physical
// key produces on the Russian ЙЦУКЕН layout (identical on macOS and Windows
// for the 26 letter keys; the punctuation keys differ between the two and are
// deliberately left out).
var jcukenLetters = map[rune]rune{
	'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н',
	'u': 'г', 'i': 'ш', 'o': 'щ', 'p': 'з', 'a': 'ф', 's': 'ы',
	'd': 'в', 'f': 'а', 'g': 'п', 'h': 'р', 'j': 'о', 'k': 'л',
	'l': 'д', 'z': 'я', 'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и',
	'n': 'т', 'm': 'ь',
}

// layoutAliasFor returns the character a single-key binding produces under a
// non-Latin keyboard layout, or "" when there is none.
//
// The overview dispatches on the rune the terminal delivers, and a terminal
// never reports which layout produced it. So with a non-Latin layout selected
// every letter hotkey is simply dead: pressing the "n" key sends "т", nothing
// matches, and the deck ignores it. That is a real cost for anyone who writes
// prompts in a non-Latin script, because the deck is the screen you come back
// to between prompts, and every visit needs a layout switch first.
//
// Registering the twin as an alias fixes it in the one place that knows it is
// the overview talking: "т" and "n" both reach new_session, while the attached
// pane keeps receiving raw bytes exactly as before. Russian ЙЦУКЕН is the
// first layout covered; the same table shape takes any other.
func layoutAliasFor(key string) string {
	runes := []rune(key)
	if len(runes) != 1 {
		return ""
	}

	twin, ok := jcukenLetters[unicode.ToLower(runes[0])]
	if !ok {
		return ""
	}
	if unicode.IsUpper(runes[0]) {
		return string(unicode.ToUpper(twin))
	}
	return string(twin)
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

// ctrlByteFromBinding converts a "ctrl+<letter>" binding to its control byte, or
// returns 0 when the binding is not a single-control-key chord. Unlike
// DetachByteFromBinding it does not fall back to Ctrl+Q, so callers can treat 0
// as "no portable byte for this key" (e.g. "ctrl+tab" / "ctrl+shift+tab", which
// have no legacy control byte).
func ctrlByteFromBinding(binding string) byte {
	binding = strings.ToLower(strings.TrimSpace(binding))
	if !strings.HasPrefix(binding, "ctrl+") {
		return 0
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
	return 0
}

// ResolvedSwitchByte returns the control byte that opens the in-attach session
// switcher for the current hotkey overrides, or 0 when it is unbound or not a
// ctrl+<letter> chord. The switcher's forward/backward cycling and commit are
// handled in the TUI, so only this single opener byte reaches the attach loop.
func ResolvedSwitchByte(overrides map[string]string) byte {
	bindings := resolveHotkeys(overrides)
	return ctrlByteFromBinding(actionHotkey(bindings, hotkeySwitchSession))
}

// ScrollbackTrigger describes how the in-attach scrollback pager is opened.
// The zero value means scrollback is disabled.
type ScrollbackTrigger struct {
	// KeyByte is a ctrl+<letter> control byte that opens the pager, or 0.
	KeyByte byte
	// OnPageUp reports whether a bare PageUp opens the pager.
	OnPageUp bool
}

// Enabled reports whether any trigger is configured.
func (t ScrollbackTrigger) Enabled() bool {
	return t.KeyByte != 0 || t.OnPageUp
}

// Label returns a short human-readable label for the configured trigger
// (e.g. "PageUp", "Ctrl+G"), or "" when disabled.
func (t ScrollbackTrigger) Label() string {
	switch {
	case t.OnPageUp:
		return "PageUp"
	case t.KeyByte != 0:
		return DetachByteLabel(t.KeyByte)
	default:
		return ""
	}
}

// ResolvedScrollbackTrigger resolves the in-attach scrollback trigger from the
// hotkey overrides. The [hotkeys].scrollback value is one of:
//   - "pageup"       — bare PageUp opens the pager (the default),
//   - "ctrl+<letter>" — that chord opens the pager,
//   - ""             — scrollback disabled.
//
// An unrecognized value falls back to the PageUp default rather than silently
// disabling the feature. It is resolved directly from overrides (not through
// resolveHotkeys) because its value is not a home-screen key and must stay out
// of the home-screen dispatch lookup.
func ResolvedScrollbackTrigger(overrides map[string]string) ScrollbackTrigger {
	val := defaultScrollbackTrigger
	for action, key := range overrides {
		if strings.TrimSpace(strings.ToLower(action)) == hotkeyScrollback {
			val = strings.TrimSpace(strings.ToLower(key))
			break
		}
	}
	switch val {
	case "":
		return ScrollbackTrigger{}
	case "pageup":
		return ScrollbackTrigger{OnPageUp: true}
	default:
		if b := ctrlByteFromBinding(val); b != 0 {
			return ScrollbackTrigger{KeyByte: b}
		}
		return ScrollbackTrigger{OnPageUp: true}
	}
}
