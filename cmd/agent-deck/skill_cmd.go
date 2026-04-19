package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func projectSkillsUnsupportedMessage() string {
	return "project skills are supported for Claude, Gemini, Codex, and Pi sessions"
}

func restartProjectSkillsSession(inst *session.Instance, jsonOutput, quietMode bool) bool {
	if inst == nil || !session.ShouldRestartProjectSkills(inst.Tool) {
		return false
	}
	if err := inst.Restart(); err != nil {
		if !jsonOutput && !quietMode {
			fmt.Fprintf(os.Stderr, "Warning: failed to restart session: %v\n", err)
		}
		return false
	}
	if session.IsClaudeCompatible(inst.Tool) {
		time.Sleep(2 * time.Second)
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			_ = tmuxSess.SendKeysAndEnter("continue")
		}
	}
	return true
}

// handleSkill handles all skill subcommands.
func handleSkill(profile string, args []string) {
	if len(args) == 0 {
		printSkillHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		handleSkillList(args[1:])
	case "attached":
		handleSkillAttached(profile, args[1:])
	case "attach":
		handleSkillAttach(profile, args[1:])
	case "detach":
		handleSkillDetach(profile, args[1:])
	case "source":
		handleSkillSource(args[1:])
	case "help", "-h", "--help":
		printSkillHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown skill command '%s'\n", args[0])
		printSkillHelp()
		os.Exit(1)
	}
}

func printSkillHelp() {
	fmt.Println("Usage: agent-deck skill <command> [options]")
	fmt.Println()
	fmt.Println("Manage project skills with project-level attach/detach and a global source registry.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list                  List discoverable skills from configured sources")
	fmt.Println("  attached [id]         Show skills attached to a session/project")
	fmt.Println("  attach <id> <skill>   Attach a skill to session project")
	fmt.Println("  detach <id> <skill>   Detach a skill from session project")
	fmt.Println("  source <cmd>          Manage global skill sources")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck skill list")
	fmt.Println("  agent-deck skill attached my-project")
	fmt.Println("  agent-deck skill attach my-project web-design-guidelines")
	fmt.Println("  agent-deck skill attach my-project react --source pool --restart")
	fmt.Println("  agent-deck skill detach my-project web-design-guidelines")
	fmt.Println("  agent-deck skill source list")
	fmt.Println("  agent-deck skill source add team ~/src/team-skills")
}

func handleSkillList(args []string) {
	fs := flag.NewFlagSet("skill list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	source := fs.String("source", "", "Filter by source name")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck skill list [options]")
		fmt.Println()
		fmt.Println("List discoverable skills from configured global sources.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	skills, err := session.ListAvailableSkills()
	if err != nil {
		out.Error(fmt.Sprintf("failed to list skills: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if strings.TrimSpace(*source) != "" {
		filtered := make([]session.SkillCandidate, 0, len(skills))
		for _, skill := range skills {
			if strings.EqualFold(skill.Source, *source) {
				filtered = append(filtered, skill)
			}
		}
		skills = filtered
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"skills": skills,
		})
		return
	}

	if quietMode {
		for _, skill := range skills {
			fmt.Println(skill.ID)
		}
		return
	}

	if len(skills) == 0 {
		fmt.Println("No skills found.")
		fmt.Println()
		fmt.Println("Try adding a source:")
		fmt.Println("  agent-deck skill source add <name> <path>")
		return
	}

	fmt.Println("Available skills:")
	fmt.Println()
	fmt.Printf("%-34s %-12s %s\n", "SKILL", "SOURCE", "DESCRIPTION")
	fmt.Println(strings.Repeat("-", 80))
	for _, skill := range skills {
		desc := skill.Description
		if desc == "" {
			desc = "-"
		}
		if len(desc) > 70 {
			desc = desc[:67] + "..."
		}
		fmt.Printf("%-34s %-12s %s\n", skill.Name, skill.Source, desc)
	}
	fmt.Printf("\nTotal: %d skills\n", len(skills))
}

func handleSkillAttached(profile string, args []string) {
	fs := flag.NewFlagSet("skill attached", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck skill attached [session-id] [options]")
		fmt.Println()
		fmt.Println("Show skills attached to a session project.")
		fmt.Println("If no session ID is provided, uses the current session (if in tmux).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return
	}

	attached, err := session.GetAttachedProjectSkills(inst.ProjectPath)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load attached skills: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	materialized, err := session.ListMaterializedProjectSkills(inst.ProjectPath)
	if err != nil {
		out.Error(fmt.Sprintf("failed to read project skills directory: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	managedTargets := make(map[string]bool)
	for _, a := range attached {
		managedTargets[strings.ToLower(strings.TrimSpace(a.TargetPath))] = true
	}
	orphansByRoot := make(map[string][]string)
	orphans := make([]session.MaterializedProjectSkill, 0)
	for _, item := range materialized {
		if managedTargets[strings.ToLower(strings.TrimSpace(item.TargetPath))] {
			continue
		}
		orphans = append(orphans, item)
		root := filepath.ToSlash(filepath.Dir(item.TargetPath))
		orphansByRoot[root] = append(orphansByRoot[root], item.EntryName)
	}
	for root := range orphansByRoot {
		sort.Strings(orphansByRoot[root])
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"session":      inst.Title,
			"session_id":   TruncateID(inst.ID),
			"project_path": inst.ProjectPath,
			"attached":     attached,
			"orphans":      orphans,
		})
		return
	}

	if quietMode {
		for _, a := range attached {
			fmt.Println(a.Name)
		}
		for _, item := range orphans {
			fmt.Println(item.TargetPath)
		}
		return
	}

	fmt.Printf("Session: %s\n", inst.Title)
	fmt.Printf("Project: %s\n\n", FormatPath(inst.ProjectPath))

	if len(attached) == 0 && len(orphans) == 0 {
		fmt.Println("No skills attached to this project.")
		return
	}

	if len(attached) > 0 {
		fmt.Printf("ATTACHED (%s):\n", FormatPath(session.GetProjectSkillsManifestPath(inst.ProjectPath)))
		for _, a := range attached {
			detail := a.Source
			if detail == "" {
				detail = "unknown"
			}
			fmt.Printf("  %s %s [%s]\n", bulletSymbol, a.Name, detail)
		}
		fmt.Println()
	}

	if len(orphans) > 0 {
		roots := make([]string, 0, len(orphansByRoot))
		for root := range orphansByRoot {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		for i, root := range roots {
			fmt.Printf("UNMANAGED (%s):\n", FormatPath(filepath.Join(inst.ProjectPath, filepath.FromSlash(root))))
			for _, name := range orphansByRoot[root] {
				fmt.Printf("  %s %s\n", bulletSymbol, name)
			}
			if i < len(roots)-1 {
				fmt.Println()
			}
		}
	}
}

func handleSkillAttach(profile string, args []string) {
	fs := flag.NewFlagSet("skill attach", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	sourceName := fs.String("source", "", "Source name override")
	restart := fs.Bool("restart", false, "Restart session to load skill immediately")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck skill attach <session-id> <skill> [options]")
		fmt.Println()
		fmt.Println("Attach a skill to the session project skills directory for the active runtime.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 2 {
		out.Error("session ID and skill name are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Println("\nUsage: agent-deck skill attach <session-id> <skill> [options]")
		}
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	skillRef := fs.Arg(1)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return
	}
	if !session.SupportsProjectSkills(inst.Tool) {
		out.Error(projectSkillsUnsupportedMessage(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	attachment, err := session.AttachSkillToProject(inst.ProjectPath, inst.Tool, skillRef, *sourceName)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSkillNotFound):
			out.Error(err.Error(), ErrCodeNotFound)
			os.Exit(2)
		case errors.Is(err, session.ErrSkillAmbiguous):
			out.Error(err.Error(), ErrCodeAmbiguous)
			os.Exit(2)
		case errors.Is(err, session.ErrSkillUnsupportedKind):
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		case errors.Is(err, session.ErrSkillAlreadyAttached):
			out.Error(err.Error(), ErrCodeAlreadyExists)
			os.Exit(1)
		default:
			out.Error(fmt.Sprintf("failed to attach skill: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	restarted := false
	if *restart {
		restarted = restartProjectSkillsSession(inst, *jsonOutput, quietMode)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success":   true,
			"session":   inst.Title,
			"skill":     attachment.Name,
			"source":    attachment.Source,
			"restarted": restarted,
		})
		return
	}

	message := fmt.Sprintf("Attached %s to %s", attachment.Name, inst.Title)
	if restarted {
		message += " - session restarted"
	}
	out.Success(message, nil)
}

func handleSkillDetach(profile string, args []string) {
	fs := flag.NewFlagSet("skill detach", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	sourceName := fs.String("source", "", "Source name filter")
	restart := fs.Bool("restart", false, "Restart session to unload skill immediately")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck skill detach <session-id> <skill> [options]")
		fmt.Println()
		fmt.Println("Detach a managed skill from the session project.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 2 {
		out.Error("session ID and skill name are required", ErrCodeInvalidOperation)
		if !*jsonOutput {
			fmt.Println("\nUsage: agent-deck skill detach <session-id> <skill> [options]")
		}
		os.Exit(1)
	}

	sessionID := fs.Arg(0)
	skillRef := fs.Arg(1)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
		return
	}
	removed, err := session.DetachSkillFromProject(inst.ProjectPath, skillRef, *sourceName)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSkillNotAttached):
			out.Error(err.Error(), ErrCodeNotFound)
			os.Exit(2)
		case errors.Is(err, session.ErrSkillAmbiguous):
			out.Error(err.Error(), ErrCodeAmbiguous)
			os.Exit(2)
		default:
			out.Error(fmt.Sprintf("failed to detach skill: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	restarted := false
	if *restart {
		restarted = restartProjectSkillsSession(inst, *jsonOutput, quietMode)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success":   true,
			"session":   inst.Title,
			"skill":     removed.Name,
			"source":    removed.Source,
			"restarted": restarted,
		})
		return
	}

	message := fmt.Sprintf("Detached %s from %s", removed.Name, inst.Title)
	if restarted {
		message += " - session restarted"
	}
	out.Success(message, nil)
}

func handleSkillSource(args []string) {
	if len(args) == 0 {
		printSkillSourceHelp()
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		handleSkillSourceList(args[1:])
	case "add":
		handleSkillSourceAdd(args[1:])
	case "remove", "rm":
		handleSkillSourceRemove(args[1:])
	case "help", "-h", "--help":
		printSkillSourceHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown skill source command '%s'\n", args[0])
		printSkillSourceHelp()
		os.Exit(1)
	}
}

func printSkillSourceHelp() {
	fmt.Println("Usage: agent-deck skill source <command> [options]")
	fmt.Println()
	fmt.Println("Manage global skill discovery sources.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list                  List configured sources")
	fmt.Println("  add <name> <path>     Add a source")
	fmt.Println("  remove <name>         Remove a source")
}

func handleSkillSourceList(args []string) {
	fs := flag.NewFlagSet("skill source list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	sources, err := session.ListSkillSources()
	if err != nil {
		out.Error(fmt.Sprintf("failed to list skill sources: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{"sources": sources})
		return
	}

	if quietMode {
		for _, source := range sources {
			fmt.Println(source.Name)
		}
		return
	}

	if len(sources) == 0 {
		fmt.Println("No skill sources configured.")
		return
	}

	fmt.Println("Skill sources:")
	fmt.Println()
	fmt.Printf("%-14s %-8s %-42s %s\n", "NAME", "ENABLED", "PATH", "DESCRIPTION")
	fmt.Println(strings.Repeat("-", 92))
	for _, source := range sources {
		enabled := "yes"
		if !source.Enabled {
			enabled = "no"
		}
		desc := source.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Printf("%-14s %-8s %-42s %s\n", source.Name, enabled, FormatPath(source.Path), desc)
	}
}

func handleSkillSourceAdd(args []string) {
	fs := flag.NewFlagSet("skill source add", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	description := fs.String("description", "", "Optional source description")

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 2 {
		out.Error("source name and path are required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	name := fs.Arg(0)
	path := fs.Arg(1)

	if err := session.AddSkillSource(name, path, *description); err != nil {
		if errors.Is(err, session.ErrSkillSourceExists) {
			out.Error(err.Error(), ErrCodeAlreadyExists)
			os.Exit(1)
		}
		out.Error(fmt.Sprintf("failed to add source: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{"success": true, "source": name})
		return
	}
	out.Success(fmt.Sprintf("Added skill source %s", name), nil)
}

func handleSkillSourceRemove(args []string) {
	fs := flag.NewFlagSet("skill source remove", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	if fs.NArg() < 1 {
		out.Error("source name is required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	name := fs.Arg(0)
	if err := session.RemoveSkillSource(name); err != nil {
		if errors.Is(err, session.ErrSkillSourceNotFound) {
			out.Error(err.Error(), ErrCodeNotFound)
			os.Exit(2)
		}
		out.Error(fmt.Sprintf("failed to remove source: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{"success": true, "source": name})
		return
	}
	out.Success(fmt.Sprintf("Removed skill source %s", name), nil)
}
