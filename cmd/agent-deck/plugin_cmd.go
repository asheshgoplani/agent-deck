// `agent-deck plugin` subcommand — first-class plugin management
// mirroring `agent-deck mcp`. All mutations route through
// session.SetField(FieldPlugins) so CLI/dialog/flag validation stays
// shared. RFC: docs/rfc/PLUGIN_ATTACH.md.

package main

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handlePlugin(profile string, args []string) {
	if len(args) == 0 {
		printPluginHelp()
		os.Exit(1)
	}
	switch args[0] {
	case "list", "ls":
		handlePluginList(args[1:])
	case "attached":
		handlePluginAttached(profile, args[1:])
	case "attach":
		handlePluginAttach(profile, args[1:])
	case "detach":
		handlePluginDetach(profile, args[1:])
	case "help", "-h", "--help":
		printPluginHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown plugin command %q\n", args[0])
		printPluginHelp()
		os.Exit(1)
	}
}

func printPluginHelp() {
	fmt.Println("Usage: agent-deck plugin <command> [options]")
	fmt.Println()
	fmt.Println("Manage Claude Code plugins (per-session enabledPlugins) for sessions.")
	fmt.Println("RFC: docs/rfc/PLUGIN_ATTACH.md")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list                   List available plugins from [plugins.<name>] in ~/.agent-deck/config.toml")
	fmt.Println("  attached [id]          Show plugins enabled on a session")
	fmt.Println("  attach <id> <name>     Enable a catalog plugin on a session")
	fmt.Println("  detach <id> <name>     Disable a catalog plugin on a session")
	fmt.Println()
	fmt.Println("Options (attach/detach):")
	fmt.Println("  --restart              Restart the session immediately so claude reloads enabledPlugins")
	fmt.Println("  --no-channel-link      For channel-emitting plugins, do NOT auto-add to Channels")
	fmt.Println("  --json                 Output as JSON")
	fmt.Println("  -q, --quiet            Minimal output")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck plugin list")
	fmt.Println("  agent-deck plugin attached my-project")
	fmt.Println("  agent-deck plugin attach my-project octopus --restart")
	fmt.Println("  agent-deck plugin detach my-project discord")
}

func handlePluginList(args []string) {
	jsonOutput, quiet := parsePluginListFlags(args)
	out := NewCLIOutput(jsonOutput, quiet)

	plugins := session.GetAvailablePlugins()
	names := session.GetAvailablePluginNames()

	if jsonOutput {
		entries := make([]map[string]interface{}, 0, len(names))
		for _, name := range names {
			def := plugins[name]
			entries = append(entries, map[string]interface{}{
				"name":          name,
				"plugin_name":   def.Name,
				"source":        def.Source,
				"id":            def.ID(),
				"emits_channel": def.EmitsChannel,
				"auto_install":  def.AutoInstall,
				"description":   def.Description,
			})
		}
		out.Print("", map[string]interface{}{"plugins": entries})
		return
	}

	if len(names) == 0 {
		fmt.Println("No plugins configured. Add [plugins.<name>] tables to ~/.agent-deck/config.toml")
		fmt.Println("RFC: docs/rfc/PLUGIN_ATTACH.md §4.1")
		return
	}
	fmt.Printf("Available plugins (%d):\n\n", len(names))
	for _, name := range names {
		def := plugins[name]
		marker := ""
		if def.EmitsChannel {
			marker = " [emits channel]"
		}
		auto := ""
		if def.AutoInstall {
			auto = " [auto-install]"
		}
		fmt.Printf("  %s%s%s\n", name, marker, auto)
		fmt.Printf("    id:     %s\n", def.ID())
		if def.Description != "" {
			fmt.Printf("    desc:   %s\n", def.Description)
		}
	}
}

func handlePluginAttached(profile string, args []string) {
	identifier, jsonOutput, quiet := parsePluginAttachedFlags(args)
	out := NewCLIOutput(jsonOutput, quiet)

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	_ = storage

	inst := resolvePluginSession(out, instances, identifier)
	if inst == nil {
		os.Exit(1)
	}

	if jsonOutput {
		out.Print("", map[string]interface{}{
			"id":                           inst.ID,
			"title":                        inst.Title,
			"plugins":                      inst.Plugins,
			"channels":                     inst.Channels,
			"plugin_channel_link_disabled": inst.PluginChannelLinkDisabled,
		})
		return
	}

	if len(inst.Plugins) == 0 {
		fmt.Printf("No plugins attached to %s\n", inst.Title)
		return
	}
	fmt.Printf("Plugins attached to %s (%d):\n\n", inst.Title, len(inst.Plugins))
	sorted := append([]string(nil), inst.Plugins...)
	sort.Strings(sorted)
	for _, name := range sorted {
		def := session.GetPluginDef(name)
		if def == nil {
			fmt.Printf("  %s  (NOT IN CATALOG — config.toml may have changed)\n", name)
			continue
		}
		fmt.Printf("  %s  (%s)\n", name, def.ID())
	}
	if inst.PluginChannelLinkDisabled {
		fmt.Println()
		fmt.Println("(auto-channel-link disabled — RFC §4.7)")
	}
}

func handlePluginAttach(profile string, args []string) {
	pluginAttachOrDetach(profile, args, "attach")
}
func handlePluginDetach(profile string, args []string) {
	pluginAttachOrDetach(profile, args, "detach")
}

func pluginAttachOrDetach(profile string, args []string, op string) {
	pos, jsonOutput, quiet, restart, noChannelLink := parsePluginAttachFlags(args)
	out := NewCLIOutput(jsonOutput, quiet)

	if len(pos) < 2 {
		out.Error(fmt.Sprintf("Usage: agent-deck plugin %s <session-id|title> <plugin-name>", op), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	identifier, name := pos[0], pos[1]

	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst := resolvePluginSession(out, instances, identifier)
	if inst == nil {
		os.Exit(1)
	}

	current := append([]string(nil), inst.Plugins...)
	updated := pluginListMutation(current, name, op)
	pluginsUnchanged := slices.Equal(current, updated)
	flagToggle := op == "attach" && noChannelLink && !inst.PluginChannelLinkDisabled

	if pluginsUnchanged && !flagToggle {
		if jsonOutput {
			out.Print("", map[string]interface{}{
				"id": inst.ID, "plugins": inst.Plugins, "noop": true,
			})
		} else {
			fmt.Printf("(%s noop) %s already %s\n", op, name, opPastTense(op))
		}
		return
	}

	// --no-channel-link is meaningful only on attach; persisting it on
	// detach would surprise the user on a later attach.
	if noChannelLink && op == "attach" {
		inst.PluginChannelLinkDisabled = true
	}

	old, _, mutErr := session.SetField(inst, session.FieldPlugins, strings.Join(updated, ","), nil)
	if mutErr != nil {
		out.Error(mutErr.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if err := saveSessionData(storage, instances, groupsData); err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	if jsonOutput {
		out.Print("", map[string]interface{}{
			"id":       inst.ID,
			"old":      old,
			"new":      strings.Join(inst.Plugins, ","),
			"plugins":  inst.Plugins,
			"channels": inst.Channels,
		})
	} else {
		fmt.Printf("✓ %s plugin %q on %s: %q -> %q\n", capitalize(opPastTense(op)), name, inst.Title, old, strings.Join(inst.Plugins, ","))
	}

	if restart {
		if !inst.CanRestart() {
			fmt.Println("(skipping --restart: session is not in a restartable state)")
			return
		}
		fmt.Println("Restarting session to apply enabledPlugins...")
		if err := inst.Restart(); err != nil {
			out.Error(fmt.Sprintf("restart failed: %s", err.Error()), ErrCodeNotFound)
			os.Exit(1)
		}
	}
}

func pluginListMutation(current []string, name, op string) []string {
	if op == "attach" {
		for _, n := range current {
			if n == name {
				return current
			}
		}
		return append(current, name)
	}
	out := make([]string, 0, len(current))
	for _, n := range current {
		if n == name {
			continue
		}
		out = append(out, n)
	}
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func opPastTense(op string) string {
	switch op {
	case "attach":
		return "attached"
	case "detach":
		return "detached"
	}
	return op
}

func resolvePluginSession(out *CLIOutput, instances []*session.Instance, identifier string) *session.Instance {
	if identifier == "" {
		if inst, _ := findSessionByTmuxAcrossProfiles(); inst != nil {
			return inst
		}
		out.Error("no session identifier given and not running inside an agent-deck tmux session", ErrCodeInvalidOperation)
		return nil
	}
	for _, inst := range instances {
		if inst.ID == identifier || inst.Title == identifier {
			return inst
		}
	}
	out.Error(fmt.Sprintf("session %q not found", identifier), ErrCodeNotFound)
	return nil
}

func parsePluginListFlags(args []string) (jsonOutput, quiet bool) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonOutput = true
		case "--quiet", "-q":
			quiet = true
		}
	}
	return
}

func parsePluginAttachedFlags(args []string) (identifier string, jsonOutput, quiet bool) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonOutput = true
		case "--quiet", "-q":
			quiet = true
		default:
			if !strings.HasPrefix(a, "-") && identifier == "" {
				identifier = a
			}
		}
	}
	return
}

func parsePluginAttachFlags(args []string) (positional []string, jsonOutput, quiet, restart, noChannelLink bool) {
	for _, a := range args {
		switch a {
		case "--json":
			jsonOutput = true
		case "--quiet", "-q":
			quiet = true
		case "--restart":
			restart = true
		case "--no-channel-link":
			noChannelLink = true
		default:
			if !strings.HasPrefix(a, "-") {
				positional = append(positional, a)
			}
		}
	}
	return
}
