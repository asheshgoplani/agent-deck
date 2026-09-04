package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteSessionCompletionTimeout bounds how long `agent-deck remote
// attach|rename <TAB>` will wait on the SSH round trip to list a remote's
// sessions. This runs on every Tab press, so it must fail fast rather than
// ride FetchSessions' normal 30s command timeout (or, per ssh.go's
// CleanStaleSSHSockets doc comment, an even longer hang against a stale
// ControlMaster socket) — a slow or unreachable remote should just leave the
// shell to fall back to plain filename completion, not freeze the terminal.
const remoteSessionCompletionTimeout = 3 * time.Second

// argKind identifies what a positional argument after a command (or
// command+subcommand) should dynamically complete to. argNone means "no
// dynamic completer" — the shell falls back to its default (filename)
// completion, which is what a real command like `add <path>` wants anyway.
type argKind int

const (
	argNone argKind = iota
	argSession
	argRemote
	argProfile
	argGroup
	argAgent
	// argRemoteSession names a session on the remote given by the preceding
	// positional argument (`remote attach <remote> <session>`, `remote
	// rename <remote> <session> <new-title>`) — plain argSession would offer
	// this machine's own local session titles, which don't exist on the
	// remote and just get in the way (a session's completion candidates and
	// its ID/title namespace are both scoped to the remote it fetched from).
	argRemoteSession
)

// completeKeyword is the string `agent-deck __complete` accepts to produce
// candidates for this kind. Keep in sync with handleComplete's switch.
func (k argKind) completeKeyword() string {
	switch k {
	case argSession:
		return "sessions"
	case argRemote:
		return "remotes"
	case argProfile:
		return "profiles"
	case argGroup:
		return "groups"
	case argAgent:
		return "agents"
	case argRemoteSession:
		return "remote-sessions"
	default:
		return ""
	}
}

// completionNode describes one node of the agent-deck command tree for
// shell-completion purposes: a top-level command name, the subcommands its
// own dispatch switch accepts (nil for leaf commands like `add` or
// `launch`), and — for the positions worth completing dynamically — which
// resource each one names.
//
// args maps a subcommand name to the ordered completers for the positional
// arguments that follow it (0-indexed). Nodes with no subcommands (subs ==
// nil) key this by "" for the args following the command name itself. A
// position past the end of the list, or a leaf command with no args entry,
// gets the shell's default completion.
//
// This is a hand-maintained mirror of the switch in main() and each
// handleX() dispatcher — the same relationship printHelp() already has to
// those switches. A new subcommand needs a one-line addition here to show
// up in completion menus; it won't be wrong to omit one, only less helpful.
type completionNode struct {
	name string
	subs []string
	args map[string][]argKind
}

// completionTree lists every user-facing agent-deck command. Pure plumbing
// invoked by other processes rather than typed by a user (hook-handler,
// codex-notify, notify-daemon, mcp-proxy, and the __complete helper below)
// is left out on purpose, matching printHelp()'s existing omission of those
// from its command list.
var completionTree = []completionNode{
	{name: "add"},
	{name: "list"},
	{name: "ls"},
	{name: "remove", args: map[string][]argKind{"": {argSession}}},
	{name: "rm", args: map[string][]argKind{"": {argSession}}},
	{name: "rename", args: map[string][]argKind{"": {argSession}}},
	{name: "mv", args: map[string][]argKind{"": {argSession}}},
	{name: "status"},
	{name: "update"},
	{name: "launch"},
	{name: "try"},
	{name: "accounts"},
	{name: "web"},
	{name: "debug-dump"},
	{name: "migrate-paths"},
	{name: "uninstall"},
	{name: "run-task"},
	{name: "feedback"},
	{name: "creds-refresh"},
	{name: "telegram-doctor"},
	{name: "version"},
	{name: "help"},
	{name: "completion", subs: []string{"bash", "zsh", "fish"}},
	{
		name: "profile",
		subs: []string{"list", "create", "delete", "default"},
		args: map[string][]argKind{
			"delete":  {argProfile},
			"default": {argProfile},
		},
	},
	{name: "inbox", subs: []string{"drain"}},
	{
		name: "session",
		subs: []string{
			"start", "stop", "remove", "cleanup", "prune", "archive", "unarchive",
			"restart", "revive", "fork", "handoff", "attach", "focus", "show",
			"current", "set-parent", "unset-parent", "update", "set-transition-notify",
			"set-title-lock", "set", "switch-account", "move", "send", "approve",
			"send-keys", "output", "children", "search",
		},
		args: map[string][]argKind{
			"start":                 {argSession},
			"stop":                  {argSession},
			"remove":                {argSession},
			"archive":               {argSession},
			"unarchive":             {argSession},
			"restart":               {argSession},
			"fork":                  {argSession},
			"handoff":               {argSession},
			"attach":                {argSession},
			"focus":                 {argSession},
			"show":                  {argSession},
			"set-parent":            {argSession, argSession},
			"unset-parent":          {argSession},
			"update":                {argSession},
			"set-transition-notify": {argSession},
			"set-title-lock":        {argSession},
			"set":                   {argSession},
			"switch-account":        {argSession, argProfile},
			"move":                  {argSession},
			"send":                  {argSession},
			"approve":               {argSession},
			"send-keys":             {argSession},
			"output":                {argSession},
			"children":              {argSession},
		},
	},
	{name: "fleet", subs: []string{"status", "recover"}},
	{
		name: "mcp",
		subs: []string{"list", "attached", "attach", "detach", "server"},
		args: map[string][]argKind{
			"attached": {argSession},
			"attach":   {argSession},
			"detach":   {argSession},
		},
	},
	{
		name: "plugin",
		subs: []string{"list", "attached", "attach", "detach"},
		args: map[string][]argKind{
			"attached": {argSession},
			"attach":   {argSession},
			"detach":   {argSession},
		},
	},
	{
		name: "skill",
		subs: []string{"list", "attached", "attach", "detach", "source"},
		args: map[string][]argKind{
			"attached": {argSession},
			"attach":   {argSession},
			"detach":   {argSession},
		},
	},
	{
		name: "group",
		subs: []string{"list", "show", "create", "update", "delete", "move", "change", "reorder"},
		args: map[string][]argKind{
			"show":    {argGroup},
			"update":  {argGroup},
			"delete":  {argGroup},
			"move":    {argSession, argGroup},
			"change":  {argGroup, argGroup},
			"reorder": {argGroup},
		},
	},
	{
		name: "conductor",
		subs: []string{"setup", "teardown", "status", "list", "move", "migrate-dir"},
		args: map[string][]argKind{
			"move": {argSession},
		},
	},
	{
		name: "agents",
		subs: []string{"adopt", "list", "show"},
		args: map[string][]argKind{"show": {argAgent}},
	},
	{
		name: "agent",
		subs: []string{"adopt", "list", "show"},
		args: map[string][]argKind{"show": {argAgent}},
	},
	{name: "watcher", subs: []string{"import", "create", "start", "stop", "list", "status", "test", "routes", "install-skill"}},
	{name: "openclaw", subs: []string{"sync", "bridge", "status", "list", "send"}},
	{name: "oc", subs: []string{"sync", "bridge", "status", "list", "send"}},
	{
		name: "remote",
		subs: []string{"add", "remove", "list", "sessions", "drain", "attach", "rename", "update"},
		args: map[string][]argKind{
			"remove":   {argRemote},
			"sessions": {argRemote},
			"attach":   {argRemote, argRemoteSession},
			"rename":   {argRemote, argRemoteSession},
			"update":   {argRemote},
		},
	},
	{
		name: "worktree",
		subs: []string{"list", "info", "cleanup", "finish", "trust-scripts"},
		args: map[string][]argKind{
			"info":   {argSession},
			"finish": {argSession},
		},
	},
	{
		name: "wt",
		subs: []string{"list", "info", "cleanup", "finish", "trust-scripts"},
		args: map[string][]argKind{
			"info":   {argSession},
			"finish": {argSession},
		},
	},
	{name: "costs", subs: []string{"sync", "summary", "recompute"}},
	{name: "hooks", subs: []string{"install", "uninstall", "status"}},
	{name: "codex-hooks", subs: []string{"install", "uninstall", "status"}},
	{name: "gemini-hooks", subs: []string{"install", "uninstall", "status"}},
	{name: "hermes-hooks", subs: []string{"install", "uninstall", "status"}},
	{name: "cursor-hooks", subs: []string{"install", "uninstall", "status"}},
	{name: "deepseek", subs: []string{"status", "profiles", "sessions"}},
}

// completionTopLevelNames returns every completable top-level command name,
// in the order completionTree declares them.
func completionTopLevelNames() []string {
	names := make([]string, len(completionTree))
	for i, n := range completionTree {
		names[i] = n.name
	}
	return names
}

// completionRule is one (command, subcommand, position) -> resource-kind
// mapping, flattened out of completionTree.args for the shell generators.
// sub is "" for a leaf command's own positional args.
type completionRule struct {
	cmd      string
	sub      string
	argIndex int
	kind     argKind
}

// completionRules flattens completionTree.args into a deterministically
// ordered list every shell generator renders identically from.
func completionRules() []completionRule {
	var rules []completionRule
	for _, n := range completionTree {
		for sub, kinds := range n.args {
			for i, k := range kinds {
				if k == argNone {
					continue
				}
				rules = append(rules, completionRule{cmd: n.name, sub: sub, argIndex: i, kind: k})
			}
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].cmd != rules[j].cmd {
			return rules[i].cmd < rules[j].cmd
		}
		if rules[i].sub != rules[j].sub {
			return rules[i].sub < rules[j].sub
		}
		return rules[i].argIndex < rules[j].argIndex
	})
	return rules
}

// handleCompletion dispatches `agent-deck completion <bash|zsh|fish>`,
// printing a self-contained completion script to stdout.
func handleCompletion(args []string) {
	if len(args) == 0 {
		printCompletionHelp(os.Stderr)
		os.Exit(1)
	}
	switch args[0] {
	case "help", "--help", "-h":
		printCompletionHelp(os.Stdout)
	case "bash":
		fmt.Print(bashCompletionScript())
	case "zsh":
		fmt.Print(zshCompletionScript())
	case "fish":
		fmt.Print(fishCompletionScript())
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown shell %q (want bash, zsh, or fish)\n\n", args[0])
		printCompletionHelp(os.Stderr)
		os.Exit(1)
	}
}

// printCompletionHelp writes usage for `agent-deck completion` to w — stdout
// for the explicit help forms, stderr when handleCompletion falls back to it
// after bad or missing arguments, so the message lands wherever the caller
// was already looking.
func printCompletionHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck completion <bash|zsh|fish>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Print a shell completion script for agent-deck's commands, subcommands,")
	fmt.Fprintln(w, "and resource names (sessions, remotes, profiles, groups, agents) to stdout.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Bash:  echo 'source <(agent-deck completion bash)' >> ~/.bashrc")
	fmt.Fprintln(w, "  Zsh:   echo 'source <(agent-deck completion zsh)' >> ~/.zshrc")
	fmt.Fprintln(w, "         (or save as a file named '_agent_deck' in a directory on $fpath)")
	fmt.Fprintln(w, "  Fish:  agent-deck completion fish > ~/.config/fish/completions/agent-deck.fish")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Open a new shell (or re-source the config file) afterwards.")
}

// handleComplete implements the internal `agent-deck __complete <kind>`
// helper the generated shell scripts shell out to for dynamic candidates.
// It is not a user-facing command — omitted from completionTree and
// printHelp, dispatched from its own case in main()'s switch.
//
// Best-effort by design: this runs on every Tab press, so a missing
// profile, a locked database, or a corrupt config must never print an
// error — that would show up as a bogus completion candidate or corrupt
// the terminal. On any failure it prints nothing and the shell falls back
// to its normal filename completion.
func handleComplete(profile string, args []string) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "sessions":
		printSessionCompletions(profile)
	case "remotes":
		printRemoteCompletions()
	case "profiles":
		printProfileCompletions()
	case "groups":
		printGroupCompletions(profile)
	case "agents":
		printAgentCompletions()
	case "remote-sessions":
		if len(args) > 1 {
			printRemoteSessionCompletions(args[1])
		}
	}
}

// completionCandidate prints name as one completion candidate, skipping
// anything empty or containing a newline (which would otherwise be split
// into two bogus candidates or corrupt the shell script's parsing of the
// output).
func completionCandidate(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return "", false
	}
	return name, true
}

// printSortedCompletions sorts names and prints each on its own line — the
// common tail every printXCompletions function below ends with.
func printSortedCompletions(names []string) {
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
}

// appendTitleOrIDCandidate falls back from title to id (the same "prefer the
// title, an untitled session is named by its ID" rule session and remote
// listings share), validates the result via completionCandidate, and skips
// it if already present in seen — the common dedup step printSessionCompletions
// and printRemoteSessionCompletions both need over their respective session
// slices.
func appendTitleOrIDCandidate(names []string, seen map[string]bool, title, id string) []string {
	name := title
	if name == "" {
		name = id
	}
	cand, ok := completionCandidate(name)
	if !ok || seen[cand] {
		return names
	}
	seen[cand] = true
	return append(names, cand)
}

// printSessionCompletions prints this machine's own local session titles
// (falling back to ID for an untitled session), one per line — the
// completer behind every plain argSession position. Any failure to open the
// profile's store is swallowed: it prints nothing rather than an error line
// a shell would otherwise offer as a bogus candidate.
func printSessionCompletions(profile string) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return
	}
	seen := make(map[string]bool)
	var names []string
	for _, inst := range instances {
		names = appendTitleOrIDCandidate(names, seen, inst.Title, inst.ID)
	}
	printSortedCompletions(names)
}

// printRemoteCompletions prints the names of every remote configured in the
// user's config file — the completer for argRemote positions like `remote
// update <TAB>`. A missing or unreadable config prints nothing rather than
// an error line.
func printRemoteCompletions() {
	config, err := session.LoadUserConfig()
	if err != nil || config == nil {
		return
	}
	names := make([]string, 0, len(config.Remotes))
	for name := range config.Remotes {
		if cand, ok := completionCandidate(name); ok {
			names = append(names, cand)
		}
	}
	printSortedCompletions(names)
}

// printRemoteSessionCompletions lists the session titles/IDs that live on
// the named remote — the completer for `remote attach`/`remote rename`'s
// session argument. Unlike printSessionCompletions this can't just read the
// local store: the session being renamed or attached to lives on the
// remote's own store, fetched over SSH (mirroring handleRemoteAttach and
// handleRemoteRename's own resolution).
func printRemoteSessionCompletions(remoteName string) {
	rc, exists, err := resolveRemoteConfig(remoteName)
	if err != nil || !exists {
		return
	}

	session.CleanStaleSSHSockets()
	runner := session.NewSSHRunner(remoteName, rc)
	ctx, cancel := context.WithTimeout(context.Background(), remoteSessionCompletionTimeout)
	defer cancel()
	sessions, err := runner.FetchSessions(ctx)
	if err != nil {
		return
	}

	seen := make(map[string]bool)
	var names []string
	for _, s := range sessions {
		names = appendTitleOrIDCandidate(names, seen, s.Title, s.ID)
	}
	printSortedCompletions(names)
}

// printProfileCompletions prints every non-internal profile name — the
// completer for argProfile positions such as `profile delete <TAB>` and the
// leading `-p <TAB>` flag. Underscore-prefixed profiles are test fixtures
// and scratch state (#1926), so they're filtered out the same way
// handleProfileList visually separates them.
func printProfileCompletions() {
	profiles, err := session.ListProfiles()
	if err != nil {
		return
	}
	var names []string
	for _, p := range profiles {
		// Underscore-prefixed profiles are test fixtures and scratch state
		// (#1926) — noise in a completion menu, so left out here the same
		// way handleProfileList visually separates them.
		if isInternalProfileName(p) {
			continue
		}
		if cand, ok := completionCandidate(p); ok {
			names = append(names, cand)
		}
	}
	printSortedCompletions(names)
}

// printGroupCompletions prints every group path in profile's session tree
// except the default group — the completer for argGroup positions like
// `group show <TAB>`. The default group is where ungrouped sessions live,
// not a real move/rename destination, so offering it would just be noise.
func printGroupCompletions(profile string) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return
	}
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		return
	}
	tree := session.NewGroupTreeWithGroups(instances, groups)
	var paths []string
	for _, g := range tree.GroupList {
		if g.Path == session.DefaultGroupPath {
			continue
		}
		if cand, ok := completionCandidate(g.Path); ok {
			paths = append(paths, cand)
		}
	}
	printSortedCompletions(paths)
}

// printAgentCompletions prints the name of every adopted agent definition —
// the completer for argAgent positions such as `agent show <TAB>`.
func printAgentCompletions() {
	defs, err := agents.LoadAll()
	if err != nil {
		return
	}
	var names []string
	for _, def := range defs {
		if cand, ok := completionCandidate(def.Name); ok {
			names = append(names, cand)
		}
	}
	printSortedCompletions(names)
}

// bashCompletionScript renders a self-contained bash completion function.
// It only needs COMP_WORDS/COMP_CWORD/compgen — no dependency on the
// separate bash-completion package.
//
// Positional args past the command (and subcommand, if any) resolve to a
// dynamic resource lookup via the "$cmd:$sub:$arg_index" case below, which
// shells out to `agent-deck __complete <kind>` — this is what lets
// `agent-deck remote update <TAB>` offer configured remote names, and
// `agent-deck session set-parent <TAB> <TAB>` offer session names at BOTH
// positions, three and four words in. A leading `-p`/`--profile` shifts
// every position that follows by 2 (or 1 for the `-p=value` form) and is
// forwarded to `__complete` so dynamic candidates match the profile being
// completed for, not just the default one.
func bashCompletionScript() string {
	var b strings.Builder
	b.WriteString("# agent-deck bash completion\n")
	b.WriteString("# Install: source <(agent-deck completion bash)\n\n")
	b.WriteString("_agent_deck_completion() {\n")
	b.WriteString("    local cur words cword start cmd sub arg_index kind\n")
	b.WriteString("    cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	b.WriteString("    words=(\"${COMP_WORDS[@]}\")\n")
	b.WriteString("    cword=$COMP_CWORD\n\n")
	b.WriteString("    # A leading -p/--profile shifts every position that follows. Complete the\n")
	b.WriteString("    # profile name itself right after it.\n")
	b.WriteString("    start=1\n")
	b.WriteString("    case \"${words[1]}\" in\n")
	b.WriteString("        -p|--profile)\n")
	b.WriteString("            if ((cword == 2)); then\n")
	b.WriteString("                local IFS=$'\\n'\n")
	b.WriteString("                COMPREPLY=($(compgen -W \"$(agent-deck __complete profiles 2>/dev/null)\" -- \"$cur\"))\n")
	b.WriteString("                return 0\n")
	b.WriteString("            fi\n")
	b.WriteString("            start=3\n")
	b.WriteString("            ;;\n")
	b.WriteString("        -p=*|--profile=*)\n")
	b.WriteString("            start=2\n")
	b.WriteString("            ;;\n")
	b.WriteString("    esac\n\n")
	b.WriteString("    if ((cword < start)); then\n")
	b.WriteString("        return 0\n")
	b.WriteString("    fi\n")
	b.WriteString("    if ((cword == start)); then\n")
	fmt.Fprintf(&b, "        COMPREPLY=($(compgen -W %s -- \"$cur\"))\n", shellescape.Quote(strings.Join(completionTopLevelNames(), " ")))
	b.WriteString("        return 0\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    cmd=${words[start]}\n")
	b.WriteString("    sub=\"\"\n")
	b.WriteString("    arg_index=$((cword - start - 1))\n\n")
	b.WriteString("    case \"$cmd\" in\n")
	for _, n := range completionTree {
		if len(n.subs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "        %s)\n", n.name)
		b.WriteString("            if ((cword == start + 1)); then\n")
		fmt.Fprintf(&b, "                COMPREPLY=($(compgen -W %s -- \"$cur\"))\n", shellescape.Quote(strings.Join(n.subs, " ")))
		b.WriteString("                return 0\n")
		b.WriteString("            fi\n")
		b.WriteString("            sub=${words[start+1]}\n")
		b.WriteString("            arg_index=$((cword - start - 2))\n")
		b.WriteString("            ;;\n")
	}
	b.WriteString("    esac\n\n")
	b.WriteString("    kind=\"\"\n")
	b.WriteString("    case \"$cmd:$sub:$arg_index\" in\n")
	for _, r := range completionRules() {
		fmt.Fprintf(&b, "        %s) kind=%s ;;\n",
			shellescape.Quote(fmt.Sprintf("%s:%s:%d", r.cmd, r.sub, r.argIndex)), r.kind.completeKeyword())
	}
	b.WriteString("    esac\n\n")
	b.WriteString("    if [[ -n $kind ]]; then\n")
	b.WriteString("        local -a profile_args=()\n")
	b.WriteString("        if ((start > 1)); then\n")
	b.WriteString("            if [[ ${words[1]} == -p || ${words[1]} == --profile ]]; then\n")
	b.WriteString("                profile_args=(-p \"${words[2]}\")\n")
	b.WriteString("            else\n")
	b.WriteString("                profile_args=(-p \"${words[1]#*=}\")\n")
	b.WriteString("            fi\n")
	b.WriteString("        fi\n")
	b.WriteString("        # remote-sessions scopes to the remote named in the preceding\n")
	b.WriteString("        # positional (`remote attach <remote> <session>`, `remote rename\n")
	b.WriteString("        # <remote> <session> <new-title>`) — forward it as an extra arg.\n")
	b.WriteString("        local -a extra_args=()\n")
	b.WriteString("        if [[ $kind == remote-sessions ]]; then\n")
	b.WriteString("            extra_args=(\"${words[start+1+arg_index]}\")\n")
	b.WriteString("        fi\n")
	b.WriteString("        local -a names=()\n")
	b.WriteString("        local n\n")
	b.WriteString("        # Read line-by-line (not `names=($(...))`) so a candidate\n")
	b.WriteString("        # containing a glob character (*, ?, [) is kept literal instead\n")
	b.WriteString("        # of being pathname-expanded against files in cwd.\n")
	b.WriteString("        while IFS= read -r n; do\n")
	b.WriteString("            names+=(\"$n\")\n")
	b.WriteString("        done < <(agent-deck \"${profile_args[@]}\" __complete \"$kind\" \"${extra_args[@]}\" 2>/dev/null)\n")
	b.WriteString("        # Candidates may contain spaces (session titles); compgen -W would\n")
	b.WriteString("        # re-split them on whitespace and lose the escaping bash needs to\n")
	b.WriteString("        # insert a multi-word completion as one argument, so filter and\n")
	b.WriteString("        # escape by hand instead of going through it.\n")
	b.WriteString("        COMPREPLY=()\n")
	b.WriteString("        for n in \"${names[@]}\"; do\n")
	b.WriteString("            if [[ $n == \"$cur\"* ]]; then\n")
	b.WriteString("                COMPREPLY+=(\"${n// /\\\\ }\")\n")
	b.WriteString("            fi\n")
	b.WriteString("        done\n")
	b.WriteString("        return 0\n")
	b.WriteString("    fi\n")
	b.WriteString("}\n")
	// -o default lets readline fall back to its own filename completion
	// whenever the function above leaves COMPREPLY empty — e.g. `add
	// <TAB>`, a leaf command with no dynamic completer at all.
	b.WriteString("complete -o default -F _agent_deck_completion agent-deck\n")
	return b.String()
}

// zshCompletionScript renders a completion function using zsh's native
// compadd, guarded so the script works both loaded directly via `source`
// and installed as an autoloaded `_agent_deck` file on $fpath. Mirrors
// bashCompletionScript's position math (words[1] == "agent-deck" here,
// versus words[0] in bash) — see its doc comment for the dynamic-arg and
// leading -p/--profile handling both share.
func zshCompletionScript() string {
	var b strings.Builder
	b.WriteString("#compdef agent-deck\n")
	b.WriteString("# agent-deck zsh completion\n\n")
	b.WriteString("_agent_deck() {\n")
	b.WriteString("    local -a top_commands subs cands profile_args extra_args\n")
	b.WriteString("    local start cmd sub arg_index kind\n")
	fmt.Fprintf(&b, "    top_commands=(%s)\n\n", strings.Join(completionTopLevelNames(), " "))
	b.WriteString("    start=2\n")
	b.WriteString("    case ${words[2]} in\n")
	b.WriteString("        -p|--profile)\n")
	b.WriteString("            if (( CURRENT == 3 )); then\n")
	b.WriteString("                cands=(${(f)\"$(agent-deck __complete profiles 2>/dev/null)\"})\n")
	b.WriteString("                compadd -a cands\n")
	b.WriteString("                return\n")
	b.WriteString("            fi\n")
	b.WriteString("            start=4\n")
	b.WriteString("            ;;\n")
	b.WriteString("        -p=*|--profile=*)\n")
	b.WriteString("            start=3\n")
	b.WriteString("            ;;\n")
	b.WriteString("    esac\n\n")
	b.WriteString("    if (( CURRENT < start )); then\n")
	b.WriteString("        return\n")
	b.WriteString("    fi\n")
	b.WriteString("    if (( CURRENT == start )); then\n")
	b.WriteString("        compadd -a top_commands\n")
	b.WriteString("        return\n")
	b.WriteString("    fi\n\n")
	b.WriteString("    cmd=${words[start]}\n")
	b.WriteString("    sub=\"\"\n")
	b.WriteString("    arg_index=$(( CURRENT - start - 1 ))\n\n")
	b.WriteString("    case $cmd in\n")
	for _, n := range completionTree {
		if len(n.subs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "    %s)\n", n.name)
		b.WriteString("        if (( CURRENT == start + 1 )); then\n")
		fmt.Fprintf(&b, "            subs=(%s)\n", strings.Join(n.subs, " "))
		b.WriteString("            compadd -a subs\n")
		b.WriteString("            return\n")
		b.WriteString("        fi\n")
		b.WriteString("        sub=${words[start+1]}\n")
		b.WriteString("        arg_index=$(( CURRENT - start - 2 ))\n")
		b.WriteString("        ;;\n")
	}
	b.WriteString("    esac\n\n")
	b.WriteString("    kind=\"\"\n")
	b.WriteString("    case \"$cmd:$sub:$arg_index\" in\n")
	for _, r := range completionRules() {
		fmt.Fprintf(&b, "    %s) kind=%s ;;\n",
			shellescape.Quote(fmt.Sprintf("%s:%s:%d", r.cmd, r.sub, r.argIndex)), r.kind.completeKeyword())
	}
	b.WriteString("    esac\n\n")
	b.WriteString("    if [[ -n $kind ]]; then\n")
	b.WriteString("        profile_args=()\n")
	b.WriteString("        if (( start > 2 )); then\n")
	b.WriteString("            if [[ ${words[2]} == -p || ${words[2]} == --profile ]]; then\n")
	b.WriteString("                profile_args=(-p ${words[3]})\n")
	b.WriteString("            else\n")
	b.WriteString("                profile_args=(-p ${words[2]#*=})\n")
	b.WriteString("            fi\n")
	b.WriteString("        fi\n")
	b.WriteString("        # remote-sessions scopes to the remote named in the preceding\n")
	b.WriteString("        # positional (`remote attach <remote> <session>`, `remote rename\n")
	b.WriteString("        # <remote> <session> <new-title>`) — forward it as an extra arg.\n")
	b.WriteString("        extra_args=()\n")
	b.WriteString("        if [[ $kind == remote-sessions ]]; then\n")
	b.WriteString("            extra_args=(${words[start+1+arg_index]})\n")
	b.WriteString("        fi\n")
	b.WriteString("        cands=(${(f)\"$(agent-deck ${profile_args[@]} __complete $kind ${extra_args[@]} 2>/dev/null)\"})\n")
	b.WriteString("    fi\n")
	b.WriteString("    # No dynamic completer applies (a leaf command's own positional args,\n")
	b.WriteString("    # e.g. `add <TAB>`) or it produced no candidates — fall back to\n")
	b.WriteString("    # filename completion, matching bash's `complete -o default`.\n")
	b.WriteString("    if (( ${#cands[@]} )); then\n")
	b.WriteString("        compadd -a cands\n")
	b.WriteString("    else\n")
	b.WriteString("        _files\n")
	b.WriteString("    fi\n")
	b.WriteString("}\n\n")
	b.WriteString("if [[ $(type compdef) = *function* ]]; then\n")
	b.WriteString("    compdef _agent_deck agent-deck\n")
	b.WriteString("fi\n")
	return b.String()
}

// fishCompletionScript renders `complete -c agent-deck` rules gated by a
// small helper (__agent_deck_at) that counts tokens up to the cursor —
// fish's own bundled completions (e.g. git.fish) use the same
// commandline-token-counting idiom. __agent_deck_toks first strips a
// leading -p/--profile pair so every position downstream lines up the same
// way regardless of whether one was typed.
func fishCompletionScript() string {
	var b strings.Builder
	b.WriteString("# agent-deck fish completion\n")
	b.WriteString("# Install: agent-deck completion fish > ~/.config/fish/completions/agent-deck.fish\n\n")

	b.WriteString("function __agent_deck_toks\n")
	b.WriteString("    set -l toks (commandline -opc)\n")
	b.WriteString("    if [ (count $toks) -ge 3 ]; and contains -- $toks[2] -p --profile\n")
	b.WriteString("        set toks $toks[1] $toks[4..-1]\n")
	b.WriteString("    else if [ (count $toks) -ge 2 ]; and string match -q -- '-p=*' $toks[2]\n")
	b.WriteString("        set toks $toks[1] $toks[3..-1]\n")
	b.WriteString("    else if [ (count $toks) -ge 2 ]; and string match -q -- '--profile=*' $toks[2]\n")
	b.WriteString("        set toks $toks[1] $toks[3..-1]\n")
	b.WriteString("    end\n")
	b.WriteString("    for t in $toks\n")
	b.WriteString("        echo $t\n")
	b.WriteString("    end\n")
	b.WriteString("end\n\n")

	b.WriteString("function __agent_deck_profile_arg\n")
	b.WriteString("    set -l toks (commandline -opc)\n")
	b.WriteString("    if [ (count $toks) -ge 3 ]; and contains -- $toks[2] -p --profile\n")
	b.WriteString("        echo -p\n")
	b.WriteString("        echo $toks[3]\n")
	b.WriteString("    else if [ (count $toks) -ge 2 ]; and string match -q -- '-p=*' $toks[2]\n")
	b.WriteString("        echo -p\n")
	b.WriteString("        echo (string replace -r '^-p=' '' $toks[2])\n")
	b.WriteString("    else if [ (count $toks) -ge 2 ]; and string match -q -- '--profile=*' $toks[2]\n")
	b.WriteString("        echo -p\n")
	b.WriteString("        echo (string replace -r '^--profile=' '' $toks[2])\n")
	b.WriteString("    end\n")
	b.WriteString("end\n\n")

	b.WriteString("function __agent_deck_at\n")
	b.WriteString("    set -l toks (__agent_deck_toks)\n")
	b.WriteString("    if [ (count $toks) -ne $argv[3] ]\n")
	b.WriteString("        return 1\n")
	b.WriteString("    end\n")
	b.WriteString("    if [ -n \"$argv[1]\" ]; and [ \"$toks[2]\" != \"$argv[1]\" ]\n")
	b.WriteString("        return 1\n")
	b.WriteString("    end\n")
	b.WriteString("    if [ -n \"$argv[2]\" ]; and [ \"$toks[3]\" != \"$argv[2]\" ]\n")
	b.WriteString("        return 1\n")
	b.WriteString("    end\n")
	b.WriteString("    return 0\n")
	b.WriteString("end\n\n")

	b.WriteString("# Profile name right after a leading -p/--profile.\n")
	b.WriteString("complete -c agent-deck -f -n \"test (count (commandline -opc)) -eq 2; and contains -- (commandline -opc)[2] -p --profile\" -a \"(agent-deck __complete profiles)\"\n\n")

	b.WriteString("# Top-level commands.\n")
	fmt.Fprintf(&b, "complete -c agent-deck -f -n \"__agent_deck_at '' '' 1\" -a \"%s\"\n\n", strings.Join(completionTopLevelNames(), " "))

	b.WriteString("# Subcommands.\n")
	for _, n := range completionTree {
		if len(n.subs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "complete -c agent-deck -f -n \"__agent_deck_at %s '' 2\" -a \"%s\"\n", shellescape.Quote(n.name), strings.Join(n.subs, " "))
	}
	b.WriteString("\n# Dynamic resource-name completion.\n")
	for _, r := range completionRules() {
		n := 2 + r.argIndex
		if r.sub != "" {
			n = 3 + r.argIndex
		}
		// remote-sessions scopes to the remote named in the preceding
		// positional (`remote attach <remote> <session>`, `remote rename
		// <remote> <session> <new-title>`) — that's always the last token
		// __agent_deck_toks has produced right before completing this one.
		extra := ""
		if r.kind == argRemoteSession {
			extra = " (__agent_deck_toks)[-1]"
		}
		fmt.Fprintf(&b, "complete -c agent-deck -f -n \"__agent_deck_at %s %s %d\" -a \"(agent-deck (__agent_deck_profile_arg) __complete %s%s)\"\n",
			shellescape.Quote(r.cmd), shellescape.Quote(r.sub), n, r.kind.completeKeyword(), extra)
	}
	return b.String()
}
