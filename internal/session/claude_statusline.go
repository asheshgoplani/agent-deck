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
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// The "accounts" field reads each slot's quota file
// (~/.cache/agent-deck/quota/<slot>/claude.json), which only
// `agent-deck usage ingest claude` writes, from a Claude Code statusLine.
// rc.6 documented that wiring and left it to the operator; no slot on any
// host had it, so every slot read "usage unknown". The usage feed is now
// installed by construction: `hooks install` (and the daemon's start-up
// heal) wraps every configured slot's statusLine command as
//
//	<agent-deck> -p <slot> usage ingest claude -- <existing command>
//
// or installs the plain ingester when the slot has none. The wrapped
// command's bytes are passed through verbatim (usage_cmd.go
// runWrappedStatusLine), the slot is named explicitly so the file lands
// under the slot's own name whatever CLAUDE_CONFIG_DIR says, and the binary
// is the same stable path the hook entries pin (hookExecutablePath).

// usageIngestWords is the subcommand the feed wrapper runs.
var usageIngestWords = []string{"usage", "ingest", "claude"}

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
	if !slices.Equal(words, usageIngestWords) {
		return feed, false
	}
	feed.Inner = strings.TrimSpace(inner)
	// sh -c '<cmd>' is how a command with shell syntax was wrapped; report
	// the command itself (only for a command this wrapper would have quoted
	// that way, so a user's own `sh -c` round-trips untouched).
	if innerWords, ok := shellwords.Split(feed.Inner); ok && len(innerWords) == 3 && innerWords[0] == "sh" && innerWords[1] == "-c" && shellSyntax(innerWords[2]) {
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
	cmd := program + " -p " + shellescape.Quote(slot) + " " + strings.Join(usageIngestWords, " ")
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return cmd
	}
	if shellSyntax(inner) {
		return cmd + usageFeedSeparator + "sh -c " + shellescape.Quote(inner)
	}
	return cmd + usageFeedSeparator + inner
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
	feed := UsageFeed{Slot: slot, ConfigDir: configDir}
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
// inner command, and a slot with no statusLine gets the plain ingester.
// Every other key and the statusLine's own siblings (padding, type) survive
// verbatim; nothing is written when nothing changes, and malformed JSON is
// an error, never overwritten. Returns whether the file was written.
func InstallUsageFeed(configDir, slot string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	root, data, err := readSettingsObject(settingsPath)
	if err != nil {
		return false, err
	}
	obj, _, err := statusLineObject(root)
	if err != nil {
		return false, err
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
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := atomicfile.WriteFile(settingsPath, finalData, 0o644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
	}
	sessionLog.Info("claude_usage_feed_installed", slog.String("config_dir", configDir), slog.String("slot", slot))
	return true, nil
}

// RemoveUsageFeed undoes InstallUsageFeed under configDir: the wrapped
// command is restored verbatim, a plain ingester entry is dropped. Returns
// whether the file was written.
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
	if parsed.Inner == "" {
		root.del("statusLine")
	} else {
		obj.set("command", mustMarshal(parsed.Inner))
		root.set("statusLine", mustMarshal(obj))
	}
	finalData, err := indentSettings(root)
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicfile.WriteFile(settingsPath, finalData, 0o644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
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

// InstallUsageFeeds wires the usage feed for every configured slot and
// reports each; one slot's failure does not stop the others.
func InstallUsageFeeds(config *UserConfig) []UsageFeedResult {
	slots := ConfiguredClaudeAccountSlots(config)
	results := make([]UsageFeedResult, 0, len(slots))
	for _, slot := range slots {
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
	slots := ConfiguredClaudeAccountSlots(config)
	results := make([]UsageFeedResult, 0, len(slots))
	for _, slot := range slots {
		changed, err := RemoveUsageFeed(slot.ConfigDir)
		results = append(results, UsageFeedResult{Slot: slot.Name, Changed: changed, Err: err})
	}
	return results
}

// UsageFeedStatuses reports every configured slot's wiring, read-only.
func UsageFeedStatuses(config *UserConfig) []UsageFeed {
	slots := ConfiguredClaudeAccountSlots(config)
	feeds := make([]UsageFeed, 0, len(slots))
	for _, slot := range slots {
		feeds = append(feeds, UsageFeedStatus(slot.ConfigDir, slot.Name))
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
	if exe, err := hookExecutablePath(); err != nil || exe == "" {
		return nil
	}
	return InstallUsageFeeds(config)
}
