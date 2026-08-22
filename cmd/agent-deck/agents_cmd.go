package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleAgents implements `agent-deck agents`: the grouped-by-machine fleet
// view. It is read-only. Nothing it prints is scheduled, started, or fired by
// agent-deck — declared triggers still belong to the plists and timers that
// own them today, and rows say so.
func handleAgents(profile string, args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	noRemote := fs.Bool("no-remote", false, "Skip remote machines")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck agents [options]")
		fmt.Println()
		fmt.Println("List adopted agents, grouped by machine.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	defs, err := agents.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Zero-config users see nothing new. An empty registry is not an error
	// and not an empty table — it is a short sentence saying how to start.
	if len(defs) == 0 {
		if *jsonOutput {
			emitJSON(agents.View{})
			return
		}
		fmt.Println("No agents adopted yet.")
		fmt.Println("Run `agent-deck agent adopt <conductor-dir|session|plist|unit>` to make an existing setup visible.")
		return
	}

	view := agents.BuildView(agents.BuildOptions{
		Definitions:   defs,
		SessionStates: loadSessionStates(profile),
		Ledger:        ledgerLookup(),
		LocalMachine:  localMachineName(),
		Remotes:       fetchRemoteAgents(*noRemote),
		Now:           time.Now(),
	})

	if *jsonOutput {
		emitJSON(view)
		return
	}
	printAgentsView(view)
}

// handleAgent implements `agent-deck agent <subcommand>`.
func handleAgent(profile string, args []string) {
	if len(args) == 0 {
		printAgentUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "adopt":
		handleAgentAdopt(profile, args[1:])
	case "list", "ls":
		handleAgents(profile, args[1:])
	case "show":
		handleAgentShow(profile, args[1:])
	case "help", "--help", "-h":
		printAgentUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown agent subcommand: %s\n\n", args[0])
		printAgentUsage()
		os.Exit(1)
	}
}

func printAgentUsage() {
	fmt.Println("Usage: agent-deck agent <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  adopt <target>   Read an existing conductor dir, session, plist or unit")
	fmt.Println("                   and emit a disabled definition for it")
	fmt.Println("  list             List adopted agents (same as `agent-deck agents`)")
	fmt.Println("  show <name>      Show one agent's definition, triggers and connectors")
	fmt.Println()
	fmt.Println("Adoption never modifies the source. It is a dry run unless you pass --write.")
}

// handleAgentAdopt introspects a target and prints the plan. Writing is a
// separate, explicit step behind --write, so the default invocation cannot
// change anything on disk.
func handleAgentAdopt(profile string, args []string) {
	fs := flag.NewFlagSet("agent adopt", flag.ExitOnError)
	write := fs.Bool("write", false, "Write the generated definitions (default: dry run)")
	jsonOutput := fs.Bool("json", false, "Output the plan as JSON")
	manager := fs.String("manager", "", "Post name that non-manager roles report to")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck agent adopt <conductor-dir|session|plist|unit> [options]")
		fmt.Println()
		fmt.Println("Introspects the target and emits a DISABLED definition with an evidence report.")
		fmt.Println("The source is never modified: no plist is unloaded, no unit is edited, no")
		fmt.Println("credential is moved, and no live session is taken over.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}

	opts := agents.Options{
		Target:          fs.Arg(0),
		ManagerPost:     *manager,
		Sessions:        loadAdoptSessions(profile),
		ConductorBlocks: loadConductorBlocks(),
		UnitDirs:        launchUnitDirs(),
		Machine:         localMachineName(),
	}

	plan, err := agents.Adopt(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOutput {
		emitJSON(plan)
	} else {
		printAdoptPlan(plan)
	}

	if !*write {
		if !*jsonOutput {
			fmt.Println()
			fmt.Println("Dry run — nothing was written. Re-run with --write to emit these definitions.")
		}
		return
	}

	// A definition that does not validate is not written. Adoption that
	// emitted a broken record and reported success would put the registry in
	// a state the reader has to defend against forever.
	for _, def := range plan.Definitions {
		if def.Findings.HasErrors() {
			fmt.Fprintf(os.Stderr, "\nError: %q did not validate; nothing was written.\n", def.Name)
			for _, f := range def.Findings {
				if f.Severity == agents.SeverityError {
					fmt.Fprintf(os.Stderr, "  %s: %s\n", f.Field, f.Message)
				}
			}
			os.Exit(1)
		}
	}

	root, err := agents.Dir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "Error: create registry: %v\n", err)
		os.Exit(1)
	}
	written, err := plan.WriteTo(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !*jsonOutput {
		fmt.Println()
		for _, dir := range written {
			fmt.Printf("wrote %s\n", dir)
		}
		fmt.Println()
		fmt.Println("These definitions are disabled. The source automation still owns every trigger above.")
	}
}

// handleAgentShow prints one agent's detail, matching the TUI detail screen.
func handleAgentShow(profile string, args []string) {
	fs := flag.NewFlagSet("agent show", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Println("Usage: agent-deck agent show <name>")
		os.Exit(1)
	}
	name := fs.Arg(0)

	defs, err := agents.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	var match *agents.Definition
	for _, def := range defs {
		if def.Name == name {
			match = def
			break
		}
	}
	if match == nil {
		fmt.Fprintf(os.Stderr, "Error: no adopted agent named %q\n", name)
		os.Exit(1)
	}

	// Build the view from the WHOLE registry, not just this definition. The
	// escalation graph can only be judged across the set: with one post in
	// hand, every legitimate manager reference reads as unknown, so every post
	// adopted with --manager — which is every post Amendment 02 describes —
	// got warned about a manager sitting in the same registry.
	view := agents.BuildView(agents.BuildOptions{
		Definitions:   defs,
		SessionStates: loadSessionStates(profile),
		Ledger:        ledgerLookup(),
		LocalMachine:  localMachineName(),
		Now:           time.Now(),
	})
	row, found := findAgentRow(view, name)
	if !found {
		fmt.Fprintf(os.Stderr, "Error: %q could not be rendered\n", name)
		os.Exit(1)
	}

	if *jsonOutput {
		emitJSON(row)
		return
	}
	printAgentDetail(row, match)
}

// --- rendering ----------------------------------------------------------

// findAgentRow locates one agent's row in a whole-fleet view.
func findAgentRow(view agents.View, name string) (agents.AgentRow, bool) {
	for _, machine := range view.Machines {
		for _, row := range machine.Agents {
			if row.Name == name {
				return row, true
			}
		}
	}
	return agents.AgentRow{}, false
}

func printAgentsView(view agents.View) {
	attention := ""
	if view.NeedAttention > 0 {
		attention = fmt.Sprintf(" · %d need attention", view.NeedAttention)
	}
	fmt.Printf("%d agents%s\n", view.TotalAgents, attention)

	now := time.Now()
	for _, machine := range view.Machines {
		fmt.Println()
		header := strings.ToUpper(machine.Name)
		switch machine.Link {
		case agents.LinkUnconfirmed:
			detail := machine.LinkDetail
			if detail == "" {
				detail = "unreachable"
			}
			fmt.Printf("%s   — UNCONFIRMED: %s\n", header, agents.SanitizeForDisplay(detail))
		case agents.LinkOK:
			if machine.LinkDetail != "" {
				fmt.Printf("%s   — link ok, %s\n", header, agents.SanitizeForDisplay(machine.LinkDetail))
			} else {
				fmt.Printf("%s   — link ok\n", header)
			}
		case agents.LinkNotContacted:
			// Never claim a round trip that did not happen.
			fmt.Printf("%s   — not contacted; placement only\n", header)
		default:
			fmt.Println(header)
		}

		for _, row := range machine.Agents {
			glyph := stateGlyph(row.State)
			role := row.Role
			if row.Class != agents.ClassAgent {
				role = string(row.Class)
			}
			line := fmt.Sprintf(" %s %-18s %-12s %-11s", glyph,
				truncateCell(agents.SanitizeForDisplay(row.Name), 18),
				truncateCell(agents.SanitizeForDisplay(role), 12), row.State)
			if last := agents.FormatLastDid(row, now); last != "" {
				line += "  last: " + truncateCell(agents.SanitizeForDisplay(last), 34)
			}
			if next := agents.FormatNextDue(row); next != "" {
				line += "  next: " + next
			}
			fmt.Println(line)
			if row.Attention != "" {
				fmt.Printf("     ! %s\n", agents.SanitizeForDisplay(row.Attention))
			}
			if row.ReportsToIssue != "" && !strings.Contains(row.Attention, row.ReportsToIssue) {
				fmt.Printf("     ! %s\n", agents.SanitizeForDisplay(row.ReportsToIssue))
			}
		}
	}

	for _, notice := range view.Notices {
		fmt.Printf("\nnote: %s\n", notice)
	}
}

func printAgentDetail(row agents.AgentRow, def *agents.Definition) {
	// Definition-sourced text, all of it untrusted.
	roleLine := agents.SanitizeForDisplay(row.Role)
	if row.RoleVersion != "" {
		roleLine += " " + agents.SanitizeForDisplay(row.RoleVersion)
	}
	fmt.Printf("%s  ·  role: %s  ·  %s",
		agents.SanitizeForDisplay(row.Name), roleLine, agents.SanitizeForDisplay(row.Harness))
	if row.Account != "" {
		fmt.Printf(" / %s", agents.SanitizeForDisplay(row.Account))
	}
	fmt.Printf("  ·  %s\n", agents.SanitizeForDisplay(row.Machine))
	fmt.Printf("post: %s  ·  reports to: %s  ·  state: %s\n",
		agents.SanitizeForDisplay(row.PostID), agents.SanitizeForDisplay(row.ReportsTo), row.State)
	// Attention now folds the escalation finding in, so guard against printing
	// the same sentence twice — the same guard printAgentsView already has.
	if row.ReportsToIssue != "" && !strings.Contains(row.Attention, row.ReportsToIssue) {
		fmt.Printf("!  %s\n", agents.SanitizeForDisplay(row.ReportsToIssue))
	}

	// The loudest facts about a row must not vanish when you drill into it.
	if row.LoadError != "" {
		fmt.Printf("\n!! this definition could not be read: %s\n", agents.SanitizeForDisplay(row.LoadError))
		fmt.Println("   nothing below is trustworthy.")
	}
	for _, violation := range row.Violations {
		fmt.Printf("\n!! %s\n", agents.SanitizeForDisplay(violation))
	}
	// Attention restates whichever problem was already printed above — the
	// load error or the first violation — so printing it again here would
	// duplicate the line.
	if row.Attention != "" && len(row.Violations) == 0 && row.LoadError == "" {
		fmt.Printf("\n!  %s\n", agents.SanitizeForDisplay(row.Attention))
	}

	if len(row.Triggers) > 0 {
		fmt.Println("\nTRIGGERS")
		for _, t := range row.Triggers {
			owner := "external: " + filepath.Base(t.ExternalSource)
			if !t.External {
				owner = "ARMED HERE — phase 1 never emits this"
			}
			fmt.Printf("  %-24s %-18s %-16s [%s]\n", agents.SanitizeForDisplay(t.Name),
				agents.SanitizeForDisplay(t.Kind), agents.SanitizeForDisplay(t.NextDueText), owner)
			if t.NextDue != nil {
				fmt.Printf("      next due %s\n", t.NextDue.Format("Mon 15:04 MST"))
			}
			if t.Note != "" {
				fmt.Printf("      %s\n", agents.SanitizeForDisplay(t.Note))
			}
		}
	}

	if len(row.Connectors) > 0 {
		fmt.Println("\nCONNECTORS")
		for _, c := range row.Connectors {
			fmt.Printf("  %s %-20s %s\n", healthGlyph(c.State),
				agents.SanitizeForDisplay(c.Name), agents.SanitizeForDisplay(c.Detail))
		}
	}

	if len(row.Recent) > 0 {
		fmt.Println("\nRECENT WORK")
		for _, entry := range row.Recent {
			fmt.Printf("  %s  %s\n", entry.At.Format("15:04"), agents.SanitizeForDisplay(entry.Summary))
		}
	}

	if def != nil && def.Role != nil {
		if len(def.Role.Spec.Policy) > 0 {
			fmt.Println("\nRULES")
			for _, policy := range def.Role.Spec.Policy {
				fmt.Printf("  · %s\n", agents.SanitizeForDisplay(policy))
			}
		}
	}

	if len(row.Unresolved) > 0 {
		fmt.Println("\nUNRESOLVED")
		for _, item := range row.Unresolved {
			fmt.Printf("  · %s\n", agents.SanitizeForDisplay(item))
		}
	}
	fmt.Println()
	switch {
	case row.LoadError != "":
		fmt.Println("This definition could not be read. agent-deck is showing what little it parsed.")
	case len(row.Violations) > 0 || row.Armed():
		// Never print "this is disabled" over data that says otherwise.
		fmt.Println("This definition claims to be ARMED, which phase 1 never emits.")
		fmt.Println("agent-deck still does not run it — but the record is not what adoption writes.")
	default:
		fmt.Println("This definition is disabled. agent-deck displays it; it does not run it.")
	}
}

func printAdoptPlan(plan *agents.Plan) {
	fmt.Printf("target: %s  (%s)\n", plan.Target, plan.TargetKind)
	for _, def := range plan.Definitions {
		post := def.Post
		fmt.Printf("\n%s\n", def.Name)
		fmt.Printf("  classification: %s\n", post.Spec.Classification)
		fmt.Printf("  role:           %s\n", post.Spec.Role.Name)
		fmt.Printf("  reports to:     %s\n", post.Spec.Placement.ReportsTo)
		fmt.Printf("  post id:        %s\n", post.Metadata.PostID)
		if post.Spec.Runtime.AdoptedSessionID != "" {
			fmt.Printf("  session:        %s (provenance only)\n", post.Spec.Runtime.AdoptedSessionID)
		}
		if len(post.Spec.Triggers) > 0 {
			fmt.Println("  triggers (declared, disabled, still fired by their source):")
			for _, t := range post.Spec.Triggers {
				schedule := t.Schedule
				if schedule == "" && t.IntervalSeconds > 0 {
					schedule = agents.DescribeInterval(t.IntervalSeconds)
				}
				fmt.Printf("    - %-22s %-16s %s\n", t.Name, t.Type, schedule)
				fmt.Printf("      owned by %s\n", t.ExternalSource)
			}
		}
		if len(post.Spec.Connectors) > 0 {
			fmt.Println("  connectors (referenced, NOT enabled):")
			for _, c := range post.Spec.Connectors {
				fmt.Printf("    - %s (%s) evidence: %s\n", c.Name, c.Kind, c.EvidencePath)
			}
		}
		if len(def.RoleFiles) > 0 {
			names := make([]string, 0, len(def.RoleFiles))
			for name := range def.RoleFiles {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Printf("  role files: %s\n", strings.Join(names, ", "))
		}
		if len(post.Spec.Unresolved) > 0 {
			fmt.Println("  unresolved:")
			for _, item := range post.Spec.Unresolved {
				fmt.Printf("    · %s\n", item)
			}
		}
		for _, f := range def.Findings {
			fmt.Printf("  %s %s: %s\n", strings.ToUpper(string(f.Severity)), f.Field, f.Message)
		}
	}
	for _, note := range plan.Notes {
		fmt.Printf("\nnote: %s\n", note)
	}
}

func stateGlyph(state agents.RunState) string {
	switch state {
	case agents.RunWorking:
		return "●"
	case agents.RunIdle:
		return "●"
	case agents.RunNeedsYou:
		return "◐"
	case agents.RunError:
		return "✕"
	case agents.RunStopped:
		return "○"
	case agents.RunNoRuntime:
		return "◍"
	default:
		return "?"
	}
}

// healthGlyph must distinguish states by SHAPE. CLI output has no color, so
// giving "ok" and "down" the same ● made a dead connector look healthy —
// the worse of the two possible confusions.
func healthGlyph(state agents.HealthState) string {
	switch state {
	case agents.HealthOK:
		return "●"
	case agents.HealthStale:
		return "◐"
	case agents.HealthDown:
		return "✕"
	default:
		return "○"
	}
}

func emitJSON(value any) {
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// --- data gathering -----------------------------------------------------

// localMachineName labels the local machine in the grouped view.
func localMachineName() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "local"
	}
	if short, _, found := strings.Cut(host, "."); found {
		return short
	}
	return host
}

// loadSessionStates reads live session status. The session store stays
// authoritative for what a runtime is doing.
func loadSessionStates(profile string) map[string]agents.SessionState {
	states := map[string]agents.SessionState{}
	storage, err := session.NewReadOnlyStorageWithProfile(profile)
	if err != nil {
		return states
	}
	defer func() { _ = storage.Close() }()

	instances, err := storage.Load()
	if err != nil {
		return states
	}
	for _, inst := range instances {
		states[inst.ID] = agents.SessionState{Status: string(inst.Status), Present: true}
	}
	return states
}

// loadAdoptSessions converts the local fleet into the shape the adoption
// resolvers accept.
func loadAdoptSessions(profile string) []agents.SessionInfo {
	storage, err := session.NewReadOnlyStorageWithProfile(profile)
	if err != nil {
		return nil
	}
	defer func() { _ = storage.Close() }()

	instances, err := storage.Load()
	if err != nil {
		return nil
	}
	infos := make([]agents.SessionInfo, 0, len(instances))
	for _, inst := range instances {
		infos = append(infos, agents.SessionInfo{
			ID:          inst.ID,
			Title:       inst.Title,
			Tool:        inst.Tool,
			Account:     inst.Account,
			GroupPath:   inst.GroupPath,
			ProjectPath: inst.ProjectPath,
			Command:     inst.Command,
			Status:      string(inst.Status),
			IsConductor: inst.IsConductor,
		})
	}
	return infos
}

// loadConductorBlocks reads the `[conductors.*]` blocks from the config the
// binary actually reads.
func loadConductorBlocks() map[string]agents.ConductorBlock {
	blocks := map[string]agents.ConductorBlock{}
	config, err := session.LoadUserConfig()
	if err != nil || config == nil {
		return blocks
	}
	source := "config.toml [conductors.*]"
	for name, overrides := range config.Conductors {
		block := agents.ConductorBlock{Name: name, Account: "", // per-conductor account overrides arrive with the accounts feature (E14); empty until it merges
			Source: source}
		switch {
		case overrides.Claude.ConfigDir != "":
			block.Tool = "claude"
		case overrides.DeepSeek.Command != "":
			block.Tool = "deepseek"
		}
		blocks[strings.ToLower(name)] = block
	}
	return blocks
}

// launchUnitDirs are the directories adoption scans for a plist or unit that
// fires a target. They are read-only inputs.
func launchUnitDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "LaunchAgents"),
		filepath.Join(home, ".config", "systemd", "user"),
		"/etc/systemd/system",
	}
}

// ledgerLookup returns recent work for a session from the completion and
// talkback ledgers. Both are existing durable records; nothing new is written.
func ledgerLookup() func(string) []agents.LedgerEntry {
	return func(sessionID string) []agents.LedgerEntry {
		var entries []agents.LedgerEntry

		// What this session itself reported when it finished.
		if entry, ok := session.ReadLedgerEntry(sessionID); ok {
			summary := entry.Summary
			if summary == "" {
				// A ledger entry without a summary still tells us the turn
				// ended and how. Say that, rather than printing a bare
				// status word that reads like a description of the work.
				summary = "reported " + entry.Status
			}
			entries = append(entries, agents.LedgerEntry{
				At: entry.FinishedAt, Summary: summary, Status: entry.Status,
			})
		}

		// What its children reported to it.
		if events, err := session.ReadInboxEventsForDisplay(sessionID); err == nil {
			for _, event := range events {
				title := event.ChildTitle
				if title == "" {
					title = event.ChildSessionID
				}
				entries = append(entries, agents.LedgerEntry{
					At:      event.Timestamp,
					Summary: fmt.Sprintf("%s → %s", title, event.ToStatus),
					Status:  event.ToStatus,
				})
			}
		}

		sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
		return entries
	}
}

// fetchRemoteAgents reports configured remotes from cached observations only.
// Phase 1 has no refresh action, so a cache miss is explicit and this default
// list path never starts a process or transport.
func fetchRemoteAgents(skip bool) []agents.RemoteMachineData {
	if skip {
		return nil
	}
	config, err := session.LoadUserConfig()
	if err != nil || config == nil || len(config.Remotes) == 0 {
		return nil
	}

	names := make([]string, 0, len(config.Remotes))
	for name := range config.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]agents.RemoteMachineData, 0, len(names))
	for _, name := range names {
		rc := config.Remotes[name]
		result = append(result, agents.RemoteMachineData{
			Name: name, Link: agents.LinkUnconfirmed,
			Detail: "no cached agents observation for " + rc.Host + "; phase 1 does not contact remotes",
		})
	}
	return result
}

// truncateCell shortens a table cell to max runes with an ellipsis. Local
// copy: the shared one lives on the context-inspector branch (PR #2011) and
// arrives with it; same 6 lines, same semantics.
func truncateCell(s string, max int) string {
	if max < 4 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
