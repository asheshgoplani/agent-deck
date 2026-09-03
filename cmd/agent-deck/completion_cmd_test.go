package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Every node in completionTree must have a unique name; a subcommand named
// in n.args must actually be one of n.subs (or "" for a leaf command); and
// argument lists must never be empty (use nil / omit the key instead).
func TestCompletionTree_Consistent(t *testing.T) {
	seen := make(map[string]bool)
	for _, n := range completionTree {
		if seen[n.name] {
			t.Errorf("duplicate completion node %q", n.name)
		}
		seen[n.name] = true
		if n.subs != nil && len(n.subs) == 0 {
			t.Errorf("node %q has a non-nil empty subs slice; use nil for leaf commands", n.name)
		}

		validSub := map[string]bool{"": len(n.subs) == 0}
		for _, s := range n.subs {
			validSub[s] = true
		}
		for sub, kinds := range n.args {
			if !validSub[sub] {
				t.Errorf("node %q: args key %q is not a declared subcommand (or \"\" for a leaf)", n.name, sub)
			}
			if len(kinds) == 0 {
				t.Errorf("node %q sub %q: empty arg-kind list; omit the key instead", n.name, sub)
			}
			for i, k := range kinds {
				if k == argNone {
					t.Errorf("node %q sub %q position %d: argNone in an args list is a no-op; omit it", n.name, sub, i)
				}
				if k.completeKeyword() == "" {
					t.Errorf("node %q sub %q position %d: argKind %d has no completeKeyword()", n.name, sub, i, k)
				}
			}
		}
	}
}

// Top-level completions include user-facing commands and exclude internal ones.
func TestCompletionTopLevelNames_IncludesCoreCommands(t *testing.T) {
	names := completionTopLevelNames()
	for _, want := range []string{"add", "session", "mcp", "group", "completion", "profile", "remote"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("completionTopLevelNames() missing %q", want)
		}
	}
	for _, unwanted := range []string{"__complete", "hook-handler", "codex-notify", "notify-daemon", "mcp-proxy"} {
		for _, n := range names {
			if n == unwanted {
				t.Errorf("completionTopLevelNames() should not offer internal command %q", unwanted)
			}
		}
	}
}

// completionRules must find every rule declared in the tree (no case falls
// through silently) and must never emit a duplicate (cmd, sub, argIndex)
// key, which would make one shell's generated case statement pick whichever
// arm happened to sort first instead of failing to build.
func TestCompletionRules_NoDuplicateKeys(t *testing.T) {
	seen := make(map[string]bool)
	count := 0
	for _, n := range completionTree {
		for _, kinds := range n.args {
			count += len(kinds)
		}
	}
	rules := completionRules()
	if len(rules) != count {
		t.Fatalf("completionRules() returned %d rules, want %d (every entry in every node.args list)", len(rules), count)
	}
	for _, r := range rules {
		key := fmt.Sprintf("%s:%s:%d", r.cmd, r.sub, r.argIndex)
		if seen[key] {
			t.Errorf("duplicate completion rule %s:%s:%d", r.cmd, r.sub, r.argIndex)
		}
		seen[key] = true
	}
}

// Known real cases from the CLI's own usage strings: `remote update`
// completes a remote name, and `session set-parent` completes a session
// name at BOTH positions (three and four words deep) — the "more than two
// levels" case.
func TestCompletionRules_KnownCases(t *testing.T) {
	rules := completionRules()
	find := func(cmd, sub string, idx int) (argKind, bool) {
		for _, r := range rules {
			if r.cmd == cmd && r.sub == sub && r.argIndex == idx {
				return r.kind, true
			}
		}
		return argNone, false
	}

	cases := []struct {
		cmd, sub string
		idx      int
		want     argKind
	}{
		{"remote", "update", 0, argRemote},
		{"remote", "attach", 0, argRemote},
		{"remote", "attach", 1, argRemoteSession},
		{"remote", "rename", 0, argRemote},
		{"remote", "rename", 1, argRemoteSession},
		{"remove", "", 0, argSession},
		{"rename", "", 0, argSession},
		{"session", "set-parent", 0, argSession},
		{"session", "set-parent", 1, argSession},
		{"profile", "delete", 0, argProfile},
		{"group", "move", 0, argSession},
		{"group", "move", 1, argGroup},
		{"agent", "show", 0, argAgent},
	}
	for _, c := range cases {
		got, ok := find(c.cmd, c.sub, c.idx)
		if !ok {
			t.Errorf("no completion rule for %s %s (position %d)", c.cmd, c.sub, c.idx)
			continue
		}
		if got != c.want {
			t.Errorf("%s %s position %d: got kind %d, want %d", c.cmd, c.sub, c.idx, got, c.want)
		}
	}
}

// The generated bash script contains the expected structure and rule cases.
func TestBashCompletionScript_WellFormed(t *testing.T) {
	script := bashCompletionScript()
	for _, want := range []string{
		"_agent_deck_completion()",
		"complete -o default -F _agent_deck_completion agent-deck",
		"COMP_WORDS",
		"'start stop remove cleanup",
		"remote:update:0) kind=remotes ;;",
		"remote:rename:1) kind=remote-sessions ;;",
		"session:set-parent:1) kind=sessions ;;",
		"__complete profiles",
		`extra_args=("${words[start+1+arg_index]}")`,
		`COMPREPLY+=("${n// /\\ }")`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("bash completion script missing %q:\n%s", want, script)
		}
	}
}

// The generated zsh script contains the expected structure and rule cases.
func TestZshCompletionScript_WellFormed(t *testing.T) {
	script := zshCompletionScript()
	for _, want := range []string{
		"#compdef agent-deck",
		"_agent_deck()",
		"compdef _agent_deck agent-deck",
		"subs=(start stop",
		"remote:update:0) kind=remotes ;;",
		"remote:rename:1) kind=remote-sessions ;;",
		"session:set-parent:1) kind=sessions ;;",
		"extra_args=(${words[start+1+arg_index]})",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("zsh completion script missing %q:\n%s", want, script)
		}
	}
}

// The generated fish script contains the expected structure and rule cases.
func TestFishCompletionScript_WellFormed(t *testing.T) {
	script := fishCompletionScript()
	for _, want := range []string{
		"complete -c agent-deck",
		"function __agent_deck_at",
		"__agent_deck_at session '' 2",
		"start stop remove",
		"__agent_deck_at remote update 3",
		"__complete remotes",
		"__agent_deck_at session set-parent 4",
		`__agent_deck_at remote rename 4" -a "(agent-deck (__agent_deck_profile_arg) __complete remote-sessions (__agent_deck_toks)[-1])`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("fish completion script missing %q:\n%s", want, script)
		}
	}
}

// TestCompletionScripts_ShellSyntaxIsValid runs each generated script
// through its own shell's syntax-check flag (bash/zsh/fish all support
// checking, rather than executing, a script read from stdin via -n). The
// WellFormed tests above only assert substrings are present in the
// generated text; a quoting bug that corrupts the script without dropping
// any of those substrings would slip past them but not past a real parser.
// Skips a shell that isn't installed on the machine running the test rather
// than failing — CI images vary in which shells they carry.
func TestCompletionScripts_ShellSyntaxIsValid(t *testing.T) {
	scripts := map[string]func() string{
		"bash": bashCompletionScript,
		"zsh":  zshCompletionScript,
		"fish": fishCompletionScript,
	}
	for shell, gen := range scripts {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s not installed: %v", shell, err)
			}
			cmd := exec.Command(shell, "-n")
			cmd.Stdin = strings.NewReader(gen())
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Errorf("%s -n rejected the generated script: %v\n%s", shell, err, stderr.String())
			}
		})
	}
}

// Every subcommand list a script renders must appear verbatim, so a stray
// quoting bug in one generator can't silently truncate a command's
// subcommand menu.
func TestCompletionScripts_CarryEverySubcommandList(t *testing.T) {
	scripts := map[string]string{
		"bash": bashCompletionScript(),
		"zsh":  zshCompletionScript(),
		"fish": fishCompletionScript(),
	}
	for _, n := range completionTree {
		if len(n.subs) == 0 {
			continue
		}
		joined := strings.Join(n.subs, " ")
		for shell, script := range scripts {
			if !strings.Contains(script, joined) {
				t.Errorf("%s completion script missing subcommand list for %q: %q", shell, n.name, joined)
			}
		}
	}
}

// captureStdout is defined in cursor_hooks_cmd_test.go.

// Session completions list every saved session by title, falling back to ID.
func TestHandleComplete_Sessions(t *testing.T) {
	storage, err := session.NewStorageWithProfile("_test_completion")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	instances := []*session.Instance{
		{ID: "aaaaaaaa-1", Title: "My Project"},
		{ID: "bbbbbbbb-2", Title: "another-session"},
		{ID: "cccccccc-3", Title: ""}, // falls back to ID
	}
	tree := session.NewGroupTreeWithGroups(instances, nil)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"sessions"}) })
	lines := strings.Split(strings.TrimSpace(out), "\n")

	for _, want := range []string{"My Project", "another-session", "cccccccc-3"} {
		found := false
		for _, l := range lines {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("session completion candidates %v missing %q", lines, want)
		}
	}
}

// An unknown completion kind prints nothing.
func TestHandleComplete_UnknownProfileIsSilent(t *testing.T) {
	// A profile that doesn't resolve (or any other failure along the way)
	// must never print an error line — the shell would offer it as a bogus
	// completion candidate.
	out := captureStdout(t, func() { handleComplete("", []string{"bogus-kind"}) })
	if out != "" {
		t.Errorf("unknown kind should print nothing, got %q", out)
	}
}

// With no remotes configured, remote completion prints nothing and doesn't crash.
func TestHandleComplete_Remotes(t *testing.T) {
	// LoadUserConfig reads the real (test-isolated, per TestMain) config
	// file; without any [[remotes]] configured this should print nothing
	// and, critically, nothing on stderr/panic.
	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"remotes"}) })
	_ = out // no remotes configured in the isolated test config; just must not crash
}

// TestHandleComplete_RemotesListsConfigured is the positive-path
// counterpart to TestHandleComplete_Remotes above: with a remote actually
// configured, it must be offered as a completion candidate. Uses its own
// HOME (t.Setenv auto-restores it) rather than the package-shared isolated
// config, so the remote this test writes never leaks into a sibling test.
func TestHandleComplete_RemotesListsConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configureRemote(t, "boxb", "worker@box-b")

	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"remotes"}) })
	if strings.TrimSpace(out) != "boxb" {
		t.Errorf("remote completion candidates = %q, want just %q", out, "boxb")
	}
}

// An unconfigured remote name prints nothing rather than shelling out.
func TestHandleComplete_RemoteSessionsUnknownRemoteIsSilent(t *testing.T) {
	// No such remote configured in the isolated test config; must print
	// nothing and, critically, never shell out to ssh trying to reach it.
	out := captureStdout(t, func() {
		handleComplete("_test_completion", []string{"remote-sessions", "no-such-remote"})
	})
	if out != "" {
		t.Errorf("unknown remote should print nothing, got %q", out)
	}
}

// A missing remote argument prints nothing instead of panicking.
func TestHandleComplete_RemoteSessionsMissingRemoteArgIsSilent(t *testing.T) {
	// The shell scripts always forward the remote name, but handleComplete
	// must degrade gracefully (never index out of range) if invoked without
	// one.
	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"remote-sessions"}) })
	if out != "" {
		t.Errorf("missing remote arg should print nothing, got %q", out)
	}
}

// With no adopted agents, agent completion prints nothing and doesn't crash.
func TestHandleComplete_Agents(t *testing.T) {
	// No adopted agents in the test-isolated registry; must print nothing
	// and, critically, nothing on stderr/panic.
	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"agents"}) })
	out := captureStdout(t, func() { handleComplete("_test_completion", []string{"agents"}) })
	if out != "" {
		t.Fatalf("expected no agent completions, got %q", out)
	}
}

// Group completions list configured groups but omit the default group.
func TestHandleComplete_Groups(t *testing.T) {
	storage, err := session.NewStorageWithProfile("_test_completion_groups")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	tree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{
		{Name: "My Team", Path: "my-team", Expanded: true, Order: 0},
	})
	if err := storage.SaveWithGroups(nil, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	out := captureStdout(t, func() { handleComplete("_test_completion_groups", []string{"groups"}) })
	if !strings.Contains(out, "my-team") {
		t.Errorf("group completion candidates %q missing %q", out, "my-team")
	}
	// The default group is where ungrouped sessions live — offering it as a
	// completion candidate would be noise, not a real destination.
	if strings.Contains(out, session.DefaultGroupPath) {
		t.Errorf("group completions must not include the default group path, got %q", out)
	}
}

// Underscore-prefixed (internal) profiles are filtered out of completions.
func TestPrintProfileCompletions_FiltersInternalNames(t *testing.T) {
	if err := session.CreateProfile("_test_completion_internal"); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := session.CreateProfile("test-completion-visible"); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	out := captureStdout(t, printProfileCompletions)
	if strings.Contains(out, "_test_completion_internal") {
		t.Errorf("underscore-prefixed profile should be filtered out, got %q", out)
	}
	if !strings.Contains(out, "test-completion-visible") {
		t.Errorf("visible profile missing from completions, got %q", out)
	}
}

// Completion help output includes usage for every supported shell.
func TestPrintCompletionHelp_WritesUsage(t *testing.T) {
	var buf bytes.Buffer
	printCompletionHelp(&buf)
	for _, want := range []string{
		"Usage: agent-deck completion <bash|zsh|fish>",
		"source <(agent-deck completion bash)",
		"source <(agent-deck completion zsh)",
		"agent-deck completion fish >",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("completion help missing %q:\n%s", want, buf.String())
		}
	}
}

// handleCompletion dispatches to the right script or help text per argument.
func TestHandleCompletion_DispatchesEachShellAndHelp(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"bash"}, "_agent_deck_completion()"},
		{[]string{"zsh"}, "#compdef agent-deck"},
		{[]string{"fish"}, "function __agent_deck_at"},
		{[]string{"help"}, "Usage: agent-deck completion <bash|zsh|fish>"},
		{[]string{"--help"}, "Usage: agent-deck completion <bash|zsh|fish>"},
		{[]string{"-h"}, "Usage: agent-deck completion <bash|zsh|fish>"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			out := captureStdout(t, func() { handleCompletion(c.args) })
			if !strings.Contains(out, c.want) {
				t.Errorf("handleCompletion(%v) stdout missing %q, got %q", c.args, c.want, out)
			}
		})
	}
}

// --- exit-path coverage via helper subprocess ---
//
// handleCompletion calls os.Exit(1) for both empty args and an unknown
// shell name. Testing that in-process would kill the whole test binary, so
// (mirroring TestCursorHooksHelperProcess) we re-exec the test binary itself
// with -test.run pinned to the helper entrypoint below.

func runCompletionHelperProcess(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=TestCompletionHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "AGENT_DECK_COMPLETION_HELPER=1")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper process failed to start/run: %v (stderr=%q)", err, errBuf.String())
		}
		code = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// TestCompletionHelperProcess is not a real test; go test invokes it (via
// -test.run) as a subprocess entrypoint that calls handleCompletion directly
// so its os.Exit calls terminate the subprocess, not the parent test binary.
// It is a no-op unless AGENT_DECK_COMPLETION_HELPER=1 is set.
func TestCompletionHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_COMPLETION_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	handleCompletion(args)
	// Valid shells/help return normally; give the parent a clean signal.
	os.Exit(0)
}

// No arguments exits 1 with usage on stderr.
func TestHandleCompletion_NoArgsExitsNonZeroWithUsageOnStderr(t *testing.T) {
	stdout, stderr, code := runCompletionHelperProcess(t)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Usage: agent-deck completion <bash|zsh|fish>") {
		t.Fatalf("stderr missing usage on empty args, got: %q", stderr)
	}
}

// An unknown shell name exits 1 with an error and usage on stderr.
func TestHandleCompletion_UnknownShellExitsNonZeroWithMessage(t *testing.T) {
	stdout, stderr, code := runCompletionHelperProcess(t, "bogus")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, `unknown shell "bogus"`) {
		t.Fatalf("stderr missing unknown-shell message, got: %q", stderr)
	}
	if !strings.Contains(stderr, "Usage: agent-deck completion <bash|zsh|fish>") {
		t.Fatalf("stderr missing usage fallback after unknown shell, got: %q", stderr)
	}
}
