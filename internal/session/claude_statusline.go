package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// Hook installation wires a statusline feed for named account configurations
// and the active default Claude config. The managed wrapper ingests metadata
// before running the previous command, or renders a default when none exists.
// Account quota files retain their existing format and cache keys.

// usageIngestWords is the subcommand the feed wrapper runs.
var usageIngestWords = []string{"usage", "ingest", "claude"}
var usageWrapWords = []string{"usage", "statusline-wrap"}

// usageFeedSeparator separates the wrapper from the wrapped command.
const usageFeedSeparator = " -- "

// UsageFeed is one slot's statusLine wiring as `hooks status` reports it.
type UsageFeed struct {
	Slot      string `json:"slot"`
	ConfigDir string `json:"config_dir"`
	// Command is the statusLine command as configured ("" when none).
	Command string `json:"command,omitempty"`
	// Wired is true when Command runs `agent-deck usage ingest claude` for
	// this slot; a wrapper feeding another slot does not count.
	Wired bool `json:"wired"`
	// Program is the agent-deck binary the wrapper names ("" when not a
	// wrapper); Slot-independent so a stale pin can be reported.
	Program string `json:"program,omitempty"`
	// FeedSlot is the slot the wrapper's -p names ("" when not a wrapper
	// or bare).
	FeedSlot string `json:"feed_slot,omitempty"`
	// Inner is the wrapped user command, verbatim ("" when the plain
	// ingester or not a wrapper).
	Inner string `json:"inner,omitempty"`
	// Blocked is why the slot cannot be wired ("" when it can): its config
	// dir does not exist (install never creates one), or its name is one
	// the quota cache cannot store (the ingester would have nowhere to
	// write). InstallUsageFeed skips such a slot; hooks status reports it.
	Blocked string `json:"blocked,omitempty"`
}

// usageFeedBlockedSlotDir is UsageFeed.Blocked for a slot whose config dir
// is not there.
const usageFeedBlockedSlotDir = "slot dir missing"

// usageFeedBlocker is UsageFeed.Blocked for slot under configDir.
func usageFeedBlocker(configDir, slot string) string {
	if err := quota.CheckProfileName(slot); err != nil {
		return err.Error()
	}
	if fi, err := os.Stat(configDir); err != nil || !fi.IsDir() {
		return usageFeedBlockedSlotDir
	}
	return ""
}

// parseUsageFeedCommand recognises a feed wrapper: program, slot named by
// -p/--profile (may be empty), and the wrapped command verbatim. ok is false
// for any other statusLine command.
func parseUsageFeedCommand(command string) (feed UsageFeed, ok bool) {
	head, inner := command, ""
	if idx := strings.Index(command, usageFeedSeparator); idx >= 0 {
		head, inner = command[:idx], command[idx+len(usageFeedSeparator):]
	}
	words, split := shellwords.Split(head)
	if !split {
		return feed, false
	}
	words = stripLeadingEnvAssignments(words)
	if len(words) == 0 || strings.TrimSuffix(filepath.Base(words[0]), ".exe") != "agent-deck" {
		return feed, false
	}
	feed.Program = words[0]
	words = words[1:]
	switch {
	case len(words) >= 2 && (words[0] == "-p" || words[0] == "--profile"):
		feed.FeedSlot, words = words[1], words[2:]
	case len(words) >= 1 && strings.HasPrefix(words[0], "-p="):
		feed.FeedSlot, words = strings.TrimPrefix(words[0], "-p="), words[1:]
	case len(words) >= 1 && strings.HasPrefix(words[0], "--profile="):
		feed.FeedSlot, words = strings.TrimPrefix(words[0], "--profile="), words[1:]
	}
	modern := slices.Equal(words, usageWrapWords)
	if !modern && !slices.Equal(words, usageIngestWords) {
		return feed, false
	}
	feed.Inner = strings.TrimSpace(inner)
	// sh -c '<cmd>' is how a command with shell syntax was wrapped; report
	// the command itself (only for a command this wrapper would have quoted
	// that way, so a user's own `sh -c` round-trips untouched).
	if innerWords, ok := shellwords.Split(feed.Inner); ok && len(innerWords) == 3 && innerWords[0] == "sh" && innerWords[1] == "-c" && (modern || shellSyntax(innerWords[2])) {
		feed.Inner = innerWords[2]
	}
	return feed, true
}

// shellSyntax reports whether a statusLine command needs a shell to mean
// what it means when run as argv: pipes, lists, redirections, subshells,
// command substitution or a leading VAR=value. Tilde and $VAR expansion are
// done by the shell that runs the wrapper itself, so they are fine as-is.
func shellSyntax(command string) bool {
	if strings.ContainsAny(command, "|&;<>()`\n") {
		return true
	}
	words, ok := shellwords.Split(command)
	if !ok || len(words) == 0 {
		return true
	}
	return isEnvAssignmentWord(words[0])
}

// usageFeedCommand builds the wrapper for slot around inner (verbatim, ""
// for the plain ingester). program is the binary to name: the pinned
// executable when this binary is pinnable, else the program an existing
// wrapper already names, else the bare "agent-deck" (see hookHandlerCommandFor).
func usageFeedCommand(slot, inner, existingProgram string) string {
	program := agentDeckHookCommandProgram(existingProgram)
	cmd := program + " -p " + shellescape.Quote(slot) + " " + strings.Join(usageWrapWords, " ")
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return cmd
	}
	return cmd + usageFeedSeparator + "sh -c " + shellescape.Quote(inner)
}

// agentDeckHookCommandProgram is the program word a fresh agent-deck entry
// uses, by the same rule as hookHandlerCommandFor: the pinned executable,
// else an existing pinned program that still exists, else bare.
func agentDeckHookCommandProgram(existing string) string {
	if exe, err := hookExecutablePath(); err == nil && exe != "" {
		return shellescape.Quote(exe)
	}
	if existing != "" && existing != "agent-deck" {
		if _, err := os.Stat(existing); err == nil {
			return shellescape.Quote(existing)
		}
	}
	return "agent-deck"
}

// statusLineObject returns settings' statusLine as an object (empty when
// absent); a statusLine that is not an object is an error rather than
// overwritten.
func statusLineObject(root jsonObject) (jsonObject, bool, error) {
	raw, ok := root.get("statusLine")
	if !ok {
		return jsonObject{}, false, nil
	}
	var obj jsonObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, true, fmt.Errorf("parse settings.json statusLine: %w", err)
	}
	return obj, true, nil
}

// UsageFeedStatus reports how slot's statusLine under configDir is wired.
// Read-only.
func UsageFeedStatus(configDir, slot string) UsageFeed {
	feed := UsageFeed{Slot: slot, ConfigDir: configDir, Blocked: usageFeedBlocker(configDir, slot)}
	root, _, err := readSettingsObject(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return feed
	}
	obj, _, err := statusLineObject(root)
	if err != nil {
		return feed
	}
	feed.Command = obj.getString("command")
	if parsed, ok := parseUsageFeedCommand(feed.Command); ok {
		feed.Program, feed.FeedSlot, feed.Inner = parsed.Program, parsed.FeedSlot, parsed.Inner
		feed.Wired = parsed.FeedSlot == slot
	}
	return feed
}

// InstallUsageFeed wires slot's statusLine under configDir to the usage
// ingester, idempotently: an existing command is wrapped once (never twice),
// a wrapper naming another binary or slot is rewritten around the same
// inner command, and a slot with no statusLine gets a default display.
// Every other key and the statusLine's own siblings (padding, type) survive
// verbatim; nothing is written when nothing changes, and malformed JSON is
// an error, never overwritten. A slot that cannot be wired (usageFeedBlocker:
// no config dir, a name the quota cache refuses) is skipped without error;
// UsageFeedStatus names the reason. Returns whether the file was written.
func InstallUsageFeed(configDir, slot string) (bool, error) {
	if usageFeedBlocker(configDir, slot) != "" {
		return false, nil
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	root, data, err := readSettingsObject(settingsPath)
	if err != nil {
		return false, err
	}
	obj, _, err := statusLineObject(root)
	if err != nil {
		return false, err
	}
	if kind := obj.getString("type"); kind != "" && kind != "command" {
		return false, fmt.Errorf("unsupported statusLine type %q; left unchanged", kind)
	}
	if raw, exists := obj.get("command"); exists {
		var command string
		if err := json.Unmarshal(raw, &command); err != nil {
			return false, fmt.Errorf("invalid statusLine command; left unchanged: %w", err)
		}
	}
	current := obj.getString("command")
	inner, existingProgram := current, ""
	if parsed, ok := parseUsageFeedCommand(current); ok {
		inner, existingProgram = parsed.Inner, parsed.Program
	}
	want := usageFeedCommand(slot, inner, existingProgram)
	if current == want {
		return false, nil
	}
	if err := saveUsageFeedBackup(configDir, root, current, want); err != nil {
		return false, err
	}
	if _, has := obj.get("type"); !has {
		obj.set("type", mustMarshal("command"))
	}
	obj.set("command", mustMarshal(want))
	root.set("statusLine", mustMarshal(obj))
	finalData, err := indentSettings(root)
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if bytes.Equal(finalData, data) {
		return false, nil
	}
	if err := atomicfile.WriteFile(settingsPath, finalData, 0o644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
	}
	sessionLog.Info("claude_usage_feed_installed", slog.String("config_dir", configDir), slog.String("slot", slot))
	return true, nil
}

// RemoveUsageFeed undoes InstallUsageFeed under configDir: the wrapped
// command is restored verbatim; a plain ingester's entry is taken out key by
// key (the command, and the "type": "command" install writes with it), and
// the statusLine object goes only when that leaves it empty, so an object
// install merely added a command to ({"type": "static", "text": ...},
// {"padding": 0}) comes back as it was. Returns whether the file was
// written.
func RemoveUsageFeed(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	root, data, err := readSettingsObject(settingsPath)
	if err != nil || data == nil {
		return false, err
	}
	obj, _, err := statusLineObject(root)
	if err != nil {
		return false, err
	}
	parsed, ok := parseUsageFeedCommand(obj.getString("command"))
	if !ok {
		return false, nil
	}
	restored, err := restoreUsageFeedBackup(configDir, &root, obj)
	if err != nil {
		return false, err
	}
	if restored {
		// The original command and type were restored from the managed backup.
	} else if parsed.Inner != "" {
		obj.set("command", mustMarshal(parsed.Inner))
		root.set("statusLine", mustMarshal(obj))
	} else {
		obj.del("command")
		if obj.getString("type") == "command" {
			obj.del("type")
		}
		if len(obj) == 0 {
			root.del("statusLine")
		} else {
			root.set("statusLine", mustMarshal(obj))
		}
	}
	finalData, err := indentSettings(root)
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicfile.WriteFile(settingsPath, finalData, 0o644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
	}
	if restored {
		_ = os.Remove(usageFeedBackupPath(configDir))
	}
	sessionLog.Info("claude_usage_feed_removed", slog.String("config_dir", configDir))
	return true, nil
}

// ClaudeAccountSlot is one configured Claude account slot: the profile
// name and its config directory.
type ClaudeAccountSlot struct {
	Name      string
	ConfigDir string
}

// ConfiguredClaudeAccountSlots lists every [profiles.<name>.claude]
// config_dir binding, sorted by name (configuredClaudeAccountNames' order)
// — the slots `accounts --json` lists and the "accounts" field reports.
func ConfiguredClaudeAccountSlots(config *UserConfig) []ClaudeAccountSlot {
	names := configuredClaudeAccountNames(config)
	slots := make([]ClaudeAccountSlot, 0, len(names))
	for _, name := range names {
		slots = append(slots, ClaudeAccountSlot{Name: name, ConfigDir: config.GetProfileClaudeConfigDir(name)})
	}
	return slots
}

// UsageFeedResult is InstallUsageFeeds' outcome for one slot.
type UsageFeedResult struct {
	Slot    string
	Changed bool
	Err     error
	Feed    UsageFeed
}

// InstallUsageFeeds wires each configured slot and the active default config and
// reports each; one slot's failure does not stop the others.
func InstallUsageFeeds(config *UserConfig) []UsageFeedResult {
	slots := ClaudeUsageFeedSlots(config)
	results := make([]UsageFeedResult, 0, len(slots))
	for _, slot := range slots {
		if config != nil && !config.Claude.GetStatuslineFeed() {
			feed := UsageFeedStatus(slot.ConfigDir, slot.Name)
			feed.Blocked = usageFeedOptedOut
			results = append(results, UsageFeedResult{Slot: slot.Name, Feed: feed})
			continue
		}
		changed, err := InstallUsageFeed(slot.ConfigDir, slot.Name)
		results = append(results, UsageFeedResult{
			Slot: slot.Name, Changed: changed, Err: err,
			Feed: UsageFeedStatus(slot.ConfigDir, slot.Name),
		})
	}
	return results
}

// RemoveUsageFeeds undoes InstallUsageFeeds for every configured slot.
func RemoveUsageFeeds(config *UserConfig) []UsageFeedResult {
	slots := ClaudeUsageFeedSlots(config)
	results := make([]UsageFeedResult, 0, len(slots))
	for _, slot := range slots {
		changed, err := RemoveUsageFeed(slot.ConfigDir)
		results = append(results, UsageFeedResult{Slot: slot.Name, Changed: changed, Err: err})
	}
	return results
}

// UsageFeedStatuses reports each discovered config's wiring, read-only.
func UsageFeedStatuses(config *UserConfig) []UsageFeed {
	slots := ClaudeUsageFeedSlots(config)
	feeds := make([]UsageFeed, 0, len(slots))
	optedOut := config != nil && !config.Claude.GetStatuslineFeed()
	for _, slot := range slots {
		feed := UsageFeedStatus(slot.ConfigDir, slot.Name)
		if optedOut && !feed.Wired && feed.Blocked == "" {
			feed.Blocked = usageFeedOptedOut
		}
		feeds = append(feeds, feed)
	}
	return feeds
}

// HealUsageFeeds is InstallUsageFeeds for the unattended paths (the notify
// daemon's start-up heal, the TUI's silent hook repair): it writes only from
// a pinnable binary, by the same rule HealClaudeHooks follows, so a dev build
// never wires every slot's statusLine to itself. Returns nil when skipped.
func HealUsageFeeds(config *UserConfig) []UsageFeedResult {
	if config == nil {
		return nil
	}
	if !config.Claude.GetStatuslineFeed() {
		return nil
	}
	if exe, err := hookExecutablePath(); err != nil || exe == "" {
		return nil
	}
	// A named account slot is wired by construction (its config binding is
	// the operator's consent). The active default config is wired only while
	// agent-deck's own hooks are installed there, so `hooks uninstall`
	// (which restores the statusLine) is not undone by the next heal.
	named := map[string]bool{}
	for _, slot := range ConfiguredClaudeAccountSlots(config) {
		named[slot.Name] = true
	}
	var results []UsageFeedResult
	for _, slot := range ClaudeUsageFeedSlots(config) {
		if !named[slot.Name] && !agentDeckHooksInstalled(slot.ConfigDir) {
			continue
		}
		changed, err := InstallUsageFeed(slot.ConfigDir, slot.Name)
		results = append(results, UsageFeedResult{
			Slot: slot.Name, Changed: changed, Err: err,
			Feed: UsageFeedStatus(slot.ConfigDir, slot.Name),
		})
	}
	return results
}

// usageFeedOptedOut is UsageFeed.Blocked when [claude] statusline_feed = false.
const usageFeedOptedOut = "disabled by [claude] statusline_feed = false"

// agentDeckHooksInstalled reports whether configDir's settings.json carries
// at least one agent-deck hook entry.
func agentDeckHooksInstalled(configDir string) bool {
	hooks, err := readClaudeHooksSection(configDir)
	return err == nil && len(distinctAgentDeckHookCommands(hooks)) > 0
}

// ClaudeUsageFeedSlots adds the active default config without inventing an
// account slot. Canonical paths prevent aliases from wrapping a file twice.
func ClaudeUsageFeedSlots(config *UserConfig) []ClaudeAccountSlot {
	slots := ConfiguredClaudeAccountSlots(config)
	seen := map[string]bool{}
	names := map[string]bool{}
	canonical := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	out := make([]ClaudeAccountSlot, 0, len(slots)+1)
	for _, slot := range slots {
		names[slot.Name] = true
		path := canonical(slot.ConfigDir)
		if !seen[path] {
			out = append(out, slot)
			seen[path] = true
		}
	}
	dir := GetClaudeConfigDir()
	if info, err := os.Stat(dir); err == nil && info.IsDir() && !seen[canonical(dir)] {
		name := "default"
		for i := 1; names[name]; i++ {
			name = fmt.Sprintf("default-claude-%d", i)
		}
		out = append(out, ClaudeAccountSlot{Name: name, ConfigDir: dir})
	}
	return out
}
