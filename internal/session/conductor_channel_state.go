// Conductor channel state: where a conductor's channel plugin (telegram,
// discord) finds its bot token, and how that survives an account switch.
//
// A channel plugin reads its state (the .env holding the bot token, access
// lists) from <PLUGIN>_STATE_DIR when set, else from
// $CLAUDE_CONFIG_DIR/channels/<plugin>. The config dir belongs to the account,
// so switching a conductor to another account used to leave the plugin with no
// token: it never started and the conductor went silent.
//
// The state belongs to the conductor, not to the account, so it is anchored
// in the conductor dir (conductor/<name>/<plugin>) and the conductor is
// pointed at it by <PLUGIN>_STATE_DIR on every start. Nothing is copied, and
// nothing is linked into an account's shared config dir: every other session
// on that account would find the token there and start a second poller on the
// same bot (409 Conflict, dropped messages).
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// channelStateEnvVars maps the channel plugins known to honour a state-dir
// variable to that variable. Plugins outside the map can only be carried by a
// channels/<plugin> link in the new config dir.
var channelStateEnvVars = map[string]string{
	"telegram": "TELEGRAM_STATE_DIR",
	"discord":  "DISCORD_STATE_DIR",
}

// channelStateAutoPoint lists the plugins whose state-dir variable agent-deck
// exports for a conductor on its own. Only TELEGRAM_* is stripped from
// sessions that do not own the channel (telegramStateDirStripExpr), so only
// telegram can be pointed without a child of the conductor inheriting the
// variable and connecting a second client to the same bot. Discord must be
// configured ([conductors.<name>.claude.env] DISCORD_STATE_DIR).
var channelStateAutoPoint = map[string]bool{"telegram": true}

// channelPlugins returns the plugin names of the session's channels
// ("plugin:telegram@claude-plugins-official" -> "telegram"), sorted, unique.
func channelPlugins(inst *Instance) []string {
	seen := map[string]bool{}
	var out []string
	for _, ch := range inst.Channels {
		rest, ok := strings.CutPrefix(ch, "plugin:")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "@")
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// channelStateUsable reports whether dir holds state the plugin can start
// from: the .env with the token for the plugins we know, any entry otherwise.
// Symlinks are followed.
func channelStateUsable(plugin, dir string) bool {
	if dir == "" {
		return false
	}
	if _, known := channelStateEnvVars[plugin]; known {
		fi, err := os.Stat(filepath.Join(dir, ".env"))
		return err == nil && fi.Mode().IsRegular()
	}
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// configuredChannelStateDir returns the value the session's configuration
// gives envVar, if any, in the prelude's precedence (later steps win):
// restart overrides, meta.json env, meta.json env_file, [conductors.<name>]
// and [groups.<group>] inline env, the tool env_file, [shell].env_files.
// env_files are read best effort (a plain `[export ]VAR=value` line); values
// produced by [shell].init_script (direnv) or a session wrapper cannot be
// known here, and still override the pointer at runtime because it is
// exported first.
func (i *Instance) configuredChannelStateDir(config *UserConfig, name, envVar string) (string, bool) {
	if v, ok := i.restartEnv[envVar]; ok {
		return ExpandPath(v), true
	}
	if meta, err := LoadConductorMeta(name); err == nil {
		if v, ok := meta.Env[envVar]; ok {
			return ExpandPath(v), true
		}
		if meta.EnvFile != "" {
			if v, ok := envFileValue(resolvePath(meta.EnvFile, i.ProjectPath), i.ProjectPath, envVar); ok {
				return v, true
			}
		}
	}
	if config != nil {
		if v, ok := config.GetConductorClaudeEnv(name)[envVar]; ok {
			return ExpandPath(v), true
		}
		if v, ok := config.GetGroupClaudeEnv(i.GroupPath)[envVar]; ok {
			return ExpandPath(v), true
		}
	}
	if f := i.getToolEnvFile(); f != "" {
		if v, ok := envFileValue(resolvePath(f, i.ProjectPath), i.ProjectPath, envVar); ok {
			return v, true
		}
	}
	if config != nil {
		for k := len(config.Shell.EnvFiles) - 1; k >= 0; k-- {
			if v, ok := envFileValue(resolvePath(config.Shell.EnvFiles[k], i.ProjectPath), i.ProjectPath, envVar); ok {
				return v, true
			}
		}
	}
	return "", false
}

// envFileValue reads a `[export ]KEY=value` line from an env file.
//
// The path comes from operator config (meta.json env_file, the tool env_file,
// [shell].env_files) and may carry expanded env-var values, so it is read only
// when it is absolute, clean, free of "..", and sits (lexically and after
// symlink resolution) under HOME or the session's project tree: the same guard
// as the env_file existence probe (validateEnvFileForProbe). A file outside
// those roots is not read here; it is still sourced by the prelude, whose
// value then overrides the conductor pointer at runtime, as for init_script.
func envFileValue(path, projectPath, key string) (string, bool) {
	clean, ok := validateEnvFileForProbe(path, projectPath)
	if !ok {
		return "", false
	}
	// Traversal barrier and root containment, colocated with the read below.
	clean = filepath.Clean(clean)
	if !filepath.IsAbs(clean) || strings.Contains(clean, "..") ||
		!containedUnderAny(clean, probeRoots(projectPath)) {
		return "", false
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		v, ok := strings.CutPrefix(line, key+"=")
		if !ok {
			continue
		}
		return ExpandPath(strings.Trim(strings.TrimSpace(v), `"'`)), true
	}
	return "", false
}

// channelAnchorPresent reports whether the anchor entry exists at all, a
// dangling link included.
func channelAnchorPresent(anchor string) bool {
	if anchor == "" {
		return false
	}
	_, err := os.Lstat(anchor)
	return err == nil
}

// conductorChannelAnchor is conductor/<name>/<plugin>.
func conductorChannelAnchor(name, plugin string) string {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, plugin)
}

// conductorChannelStateEnv returns the prelude exports pointing a conductor's
// channel plugins at conductor/<name>/<plugin>, for each plugin whose state
// dir is not configured and whose anchor exists. An anchor without state (a
// link whose old-account target went away) is still exported: the plugin
// then fails closed instead of falling back to the account's channels dir,
// and ChannelStateWarnings names it. The conductor's own
// state wins over the account's channels/<plugin>: that dir may hold another
// session's bot. It runs before every configured source, so an env_file or
// inline env that sets the variable still wins, while a value inherited from
// the tmux server (possibly another conductor's) does not.
// Conductors only: the conductor is the single consumer of its bot token.
func (i *Instance) conductorChannelStateEnv(config *UserConfig) string {
	name := i.ResolveConductorName()
	if name == "" || !instInvokesClaudeBinary(i) {
		return ""
	}
	var parts []string
	for _, plugin := range channelPlugins(i) {
		envVar, known := channelStateEnvVars[plugin]
		if !known || !channelStateAutoPoint[plugin] {
			continue
		}
		if _, ok := i.configuredChannelStateDir(config, name, envVar); ok {
			continue
		}
		anchor := conductorChannelAnchor(name, plugin)
		if !channelAnchorPresent(anchor) {
			continue
		}
		parts = append(parts, fmt.Sprintf("export %s='%s'", envVar, strings.ReplaceAll(anchor, "'", "'\\''")))
	}
	return strings.Join(parts, " && ")
}

// ChannelStateWarnings returns one line per channel plugin of a conductor
// that has no state where the plugin will look, so the plugin has no token
// and never starts. Shown by `session show` and `conductor status`.
func (i *Instance) ChannelStateWarnings() []string {
	if i == nil || !IsClaudeCompatible(i.Tool) {
		return nil
	}
	name := i.ResolveConductorNameThreadSafe()
	if name == "" {
		return nil
	}
	config, _ := LoadUserConfig()
	configDir := ExpandPath(GetClaudeConfigDirForInstance(i))
	var warnings []string
	for _, plugin := range channelPlugins(i) {
		envVar, known := channelStateEnvVars[plugin]
		dir := filepath.Join(configDir, "channels", plugin)
		hint := "this plugin has no known state-dir variable; give it state in " + dir + " only if no other session on this account runs it"
		if known {
			hint = "set " + envVar + " in [conductors." + name + ".claude.env] or put the state in " + conductorChannelAnchor(name, plugin)
			if !channelStateAutoPoint[plugin] {
				hint = "set " + envVar + " in [conductors." + name + ".claude.env]"
			}
			if v, ok := i.configuredChannelStateDir(config, name, envVar); ok {
				dir = v
				hint = "fix " + envVar
			} else if anchor := conductorChannelAnchor(name, plugin); channelStateAutoPoint[plugin] && channelAnchorPresent(anchor) {
				dir = anchor
			}
		}
		if channelStateUsable(plugin, dir) {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("channel plugin %s has no state at %s: the plugin has no token and will not start (account %q); %s", plugin, dir, i.Account, hint))
	}
	return warnings
}

// carryConductorChannelState runs when a conductor's account changes, before
// the target starts. For each channel plugin whose state lived in the old
// config dir (sourceDir/channels/<plugin>) it keeps that state reachable for
// this conductor only, without copying:
//   - telegram (channelStateAutoPoint), unless its variable is configured:
//     link conductor/<name>/<plugin> to the old state;
//     conductorChannelStateEnv then points the conductor at it on every
//     start, whatever the account;
//   - other plugins: nothing is linked. A link in the new account's shared
//     config dir would hand the token to every session on that account (a
//     second poller on the same bot), so the result says what to do instead.
//
// Existing entries are never replaced. Returns a line per action or problem
// for the switch result.
func carryConductorChannelState(inst *Instance, sourceDir, targetDir string) []string {
	if inst == nil || sourceDir == "" || targetDir == "" {
		return nil
	}
	name := inst.ResolveConductorName()
	if name == "" {
		return nil
	}
	config, _ := LoadUserConfig()
	sourceDir, targetDir = ExpandPath(sourceDir), ExpandPath(targetDir)
	var notes []string
	for _, plugin := range channelPlugins(inst) {
		old := filepath.Join(sourceDir, "channels", plugin)
		if !channelStateUsable(plugin, old) {
			continue
		}
		dst := filepath.Join(targetDir, "channels", plugin)
		dstOther := channelStateUsable(plugin, dst) && !sameChannelState(old, dst)
		envVar, known := channelStateEnvVars[plugin]
		if !known {
			if !channelStateUsable(plugin, dst) {
				notes = append(notes, fmt.Sprintf("channel %s: state stays at %s and was NOT linked into %s (every session on that account would read the token); the plugin has no known state-dir variable, so the conductor has no %s state on this account", plugin, old, dst, plugin))
			} else if dstOther {
				notes = append(notes, fmt.Sprintf("channel %s: the new account has its own state at %s; the conductor will use it (and may share that bot with another session), not %s", plugin, dst, old))
			}
			continue
		}
		if _, ok := inst.configuredChannelStateDir(config, name, envVar); ok {
			continue
		}
		if !channelStateAutoPoint[plugin] {
			if !channelStateUsable(plugin, dst) {
				real, err := filepath.EvalSymlinks(old)
				if err != nil {
					real = old
				}
				notes = append(notes, fmt.Sprintf("channel %s: state stays at %s and was NOT linked into %s; set %s = %q in [conductors.%s.claude.env] (not set automatically: %s is not stripped from child sessions, which would connect a second client)", plugin, old, dst, envVar, real, name, envVar))
			} else if dstOther {
				notes = append(notes, fmt.Sprintf("channel %s: the new account has its own state at %s; the conductor will use it (and may share that bot with another session), not %s", plugin, dst, old))
			}
			continue
		}
		anchor := conductorChannelAnchor(name, plugin)
		if channelStateUsable(plugin, anchor) {
			if !sameChannelState(old, anchor) {
				notes = append(notes, fmt.Sprintf("channel %s: conductor state %s is used, not %s", plugin, anchor, old))
			}
			continue
		}
		real, err := filepath.EvalSymlinks(old)
		if err != nil {
			notes = append(notes, fmt.Sprintf("channel %s: cannot resolve %s: %v; state not carried", plugin, old, err))
			continue
		}
		if _, err := os.Lstat(anchor); err == nil {
			notes = append(notes, fmt.Sprintf("channel %s: %s exists without state; left as is, state %s not carried", plugin, anchor, real))
			continue
		}
		if err := os.Symlink(real, anchor); err != nil {
			notes = append(notes, fmt.Sprintf("channel %s: link %s: %v; state not carried", plugin, anchor, err))
			continue
		}
		note := fmt.Sprintf("channel %s: linked %s -> %s (no copy); %s points the conductor there", plugin, anchor, real, envVar)
		if dstOther {
			note += fmt.Sprintf("; the new account's own %s is left to its sessions", dst)
		}
		notes = append(notes, note)
	}
	return notes
}

// CarryConductorChannelState is carryConductorChannelState for the
// `session set <id> account <slot>` path, which changes the account outside
// the switch executor.
func CarryConductorChannelState(inst *Instance, sourceDir, targetDir string) []string {
	return carryConductorChannelState(inst, sourceDir, targetDir)
}

func sameChannelState(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}
