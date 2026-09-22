// Package main behaviour-freeze goldens.
//
// This suite records TODAY's CLI behaviour (stdout + exit code) for every
// top-level command and subcommand that is read-only or safely repeatable in
// a sandbox, plus --help for literally every command (help text never
// touches tmux/ssh/network, so it is always safe to run). The coming
// command-registry refactor (CORE-PLAN.md section 7) must reproduce these
// goldens byte for byte; a diff is a behaviour change and needs a reviewer's
// PASS (see testdata/goldens/README.md).
//
// Regenerate with: AGENTDECK_UPDATE_GOLDENS=1 go test ./cmd/agent-deck/ -run TestCLIGoldens
// (or `make goldens-update`).
package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// updateGoldensFlag is a `go test`-recognized flag alternative to the
// AGENTDECK_UPDATE_GOLDENS env var: some CI/sandbox runners invoke `go test`
// with a fixed argument list but no ability to set extra env vars, so both
// triggers are honored (see shouldUpdateGoldens).
var updateGoldensFlag = flag.Bool("update-goldens", false, "regenerate CLI/storage-bytes goldens instead of asserting them")

func shouldUpdateGoldens() bool {
	return *updateGoldensFlag || os.Getenv("AGENTDECK_UPDATE_GOLDENS") != ""
}

// goldenSpec describes one CLI invocation whose output is frozen.
type goldenSpec struct {
	// name becomes the golden file's basename; must be unique and filesystem-safe.
	name string
	// args is the full argument list passed to the built binary (not including argv[0]).
	args []string
	// wantExit is the exit code asserted before the golden comparison runs, so a
	// crash shows up as "exit code changed" instead of a confusing text diff.
	wantExit int
}

// excludedCommand documents a top-level command or subcommand that PROMPT.md
// deliverable 1 excludes from execution because it would touch tmux/ssh/
// network, mutate the seeded store non-idempotently, or read real host state
// outside the sandbox. Its --help form is still golden-tested (see
// helpOnlySpecs) since printing usage never does any of those things.
type excludedCommand struct {
	path   string // e.g. "session start" or "remote sessions"
	reason string
}

// excludedCommands is deliverable 4's "M excluded with reasons" list.
// Every command here still gets a --help golden via helpOnlySpecs.
var excludedCommands = []excludedCommand{
	{"add", "mutates the store (creates a real tmux session); not idempotent"},
	{"launch", "mutates the store and spawns tmux/an agent process"},
	{"remove", "mutates the store (deletes a session)"},
	{"rm", "alias of remove"},
	{"rename", "mutates the store"},
	{"mv", "alias of rename"},
	{"try", "creates a dated folder + session; path depends on today's date, not idempotent"},
	{"update", "contacts the GitHub releases API (network)"},
	{"web", "starts a long-running HTTP server / daemon"},
	{"mcp-proxy", "starts a long-running proxy daemon over a socket"},
	{"uninstall", "destructive: removes installed files"},
	{"migrate-paths", "mutates on-disk layout (copies legacy files into XDG paths)"},
	{"hook-handler", "reads a hook payload from stdin; not a normal CLI invocation"},
	{"codex-notify", "reads a hook payload from stdin; not a normal CLI invocation"},
	{"notify-daemon", "starts a long-running daemon"},
	{"run-task", "executes an arbitrary configured task"},
	{"telegram-doctor", "contacts the Telegram API (network)"},
	{"creds-refresh", "refreshes OAuth credentials (network)"},
	{"feedback", "sends telemetry/feedback to a remote endpoint"},
	{"debug-dump", "captures live PIDs/goroutine state into a ring-buffer dump; not byte-stable by design"},
	{"recall", "unbounded scan of real Claude conversation transcripts on the host, outside the sandbox"},
	{"session context", "walks the real host CLAUDE.md/AGENTS.md memory hierarchy outside the sandbox (see session_context_golden_test.go)"},
	{"session start", "starts a real tmux process; covered instead by the storage-bytes goldens (deliverable 2)"},
	{"session stop", "stops a real tmux process; covered instead by the storage-bytes goldens (deliverable 2)"},
	{"session restart", "restarts a real tmux process; covered instead by the storage-bytes goldens (deliverable 2)"},
	{"session fork", "spawns a new tmux session"},
	{"session attach", "requires an interactive TTY attach to tmux"},
	{"fleet recover", "mutates the store (kills/relaunches sessions)"},
	{"group create", "mutates the store"},
	{"group update", "mutates the store"},
	{"group delete", "mutates the store"},
	{"group move", "mutates the store"},
	{"group change", "mutates the store"},
	{"group reorder", "mutates the store"},
	{"remote add", "mutates config.toml"},
	{"remote remove", "mutates config.toml"},
	{"remote rename", "SSHes to the remote host"},
	{"remote attach", "SSHes to the remote host for an interactive attach"},
	{"remote sessions", "SSHes to the remote host to list its sessions"},
	{"remote update", "SSHes to the remote host and installs a binary"},
	{"worktree cleanup", "mutates the filesystem (removes orphaned worktrees)"},
	{"mcp attach", "mutates the store"},
	{"mcp detach", "mutates the store"},
	{"skill attach", "mutates the store"},
	{"skill detach", "mutates the store"},
	{"codex-hooks install", "mutates ~/.codex hook config"},
	{"codex-hooks uninstall", "mutates ~/.codex hook config"},
	{"gemini-hooks install", "mutates Gemini hook config"},
	{"gemini-hooks uninstall", "mutates Gemini hook config"},
	{"hermes-hooks install", "mutates Hermes hook config"},
	{"hermes-hooks uninstall", "mutates Hermes hook config"},
	{"cursor-hooks install", "mutates Cursor hook config"},
	{"cursor-hooks uninstall", "mutates Cursor hook config"},
	{"tmux-hooks install", "mutates the live tmux server's hook slot"},
	{"tmux-hooks uninstall", "mutates the live tmux server's hook slot"},
	{"pi-hooks install", "mutates the pi extension install"},
	{"pi-hooks uninstall", "mutates the pi extension install"},
	{"profile create", "mutates the profile catalog"},
	{"profile delete", "mutates the profile catalog"},
	{"conductor setup", "starts a Telegram bridge daemon"},
	{"conductor teardown", "stops a daemon / mutates state"},
	{"inbox drain", "mutates the inbox (consumes queued events)"},
	{"watcher create", "mutates config + starts a watcher process"},
	{"watcher start", "starts a watcher process"},
	{"watcher stop", "stops a watcher process"},
	{"agent adopt", "mutates the local agent catalog"},
	{"system stats", "reports live host CPU/load/memory/disk numbers; not byte-stable by design, unlike a scrubbable timestamp"},
	{"costs sync", "mutates cost_events (imports usage from provider logs)"},
	{"costs recompute", "mutates cost_events (rewrites cost_microdollars; --dry-run form is the safe one but not the default)"},
	{"watcher import", "requires a positional file argument before flags are parsed, so --help alone is not a safe probe; also mutates config"},
	{"watcher install-skill", "requires a positional skill argument before flags are parsed, so --help alone is not a safe probe; also mutates the filesystem"},
}

// safeSpecs is deliverable 1's "N commands covered" list: every top-level
// command and subcommand that is read-only or safely repeatable against the
// seeded store, run with --json where the command supports it. --help for
// every one of these (and for every excluded command above) lives in
// helpSpecs below.
func safeSpecs() []goldenSpec {
	return []goldenSpec{
		{"version", []string{"version"}, 0},
		{"list", []string{"-p", goldensProfile, "list"}, 0},
		{"list_json", []string{"-p", goldensProfile, "list", "--json"}, 0},
		{"status", []string{"-p", goldensProfile, "status"}, 0},
		{"accounts", []string{"accounts"}, 0},
		{"doctor", []string{"doctor"}, 0},
		{"health_json", []string{"-p", goldensProfile, "health", "--json"}, 0},
		{"usage", []string{"-p", goldensProfile, "usage"}, 0},
		{"costs_summary", []string{"-p", goldensProfile, "costs", "summary"}, 0},
		{"agents", []string{"agents"}, 0},
		{"telemetry_status", []string{"telemetry", "status"}, 0},

		{"session_show", []string{"-p", goldensProfile, "session", "show", "golden-sess-1"}, 0},
		{"session_viewers", []string{"-p", goldensProfile, "session", "viewers", "golden-sess-1"}, 0},

		{"fleet_status", []string{"-p", goldensProfile, "fleet", "status"}, 0},

		{"mcp_list", []string{"-p", goldensProfile, "mcp", "list"}, 0},
		{"mcp_list_json", []string{"-p", goldensProfile, "mcp", "list", "--json"}, 0},
		{"mcp_attached", []string{"-p", goldensProfile, "mcp", "attached", "golden-sess-1"}, 0},

		{"skill_list", []string{"-p", goldensProfile, "skill", "list"}, 0},
		{"skill_attached", []string{"-p", goldensProfile, "skill", "attached", "golden-sess-1"}, 0},
		{"skill_source_list", []string{"-p", goldensProfile, "skill", "source", "list"}, 0},

		{"codex_hooks_status", []string{"codex-hooks", "status"}, 0},
		{"gemini_hooks_status", []string{"gemini-hooks", "status"}, 0},
		{"hermes_hooks_status", []string{"hermes-hooks", "status"}, 0},
		{"cursor_hooks_status", []string{"cursor-hooks", "status"}, 0},
		// exit 1: no tmux server is running yet in a fresh sandbox (today's real behaviour).
		{"tmux_hooks_status", []string{"tmux-hooks", "status"}, 1},
		{"pi_hooks_status", []string{"pi-hooks", "status"}, 0},
		{"deepseek_status", []string{"deepseek", "status"}, 0},
		{"deepseek_profiles", []string{"deepseek", "profiles"}, 0},

		{"group_list", []string{"-p", goldensProfile, "group", "list"}, 0},
		{"group_show", []string{"-p", goldensProfile, "group", "show", "backend"}, 0},
		{"group_show_resolved", []string{"-p", goldensProfile, "group", "show", "backend", "--resolved"}, 0},

		{"conductor_status", []string{"-p", goldensProfile, "conductor", "status"}, 0},
		{"conductor_list", []string{"-p", goldensProfile, "conductor", "list"}, 0},

		{"remote_list", []string{"-p", goldensProfile, "remote", "list"}, 0},
		{"remote_list_json", []string{"-p", goldensProfile, "remote", "list", "--json"}, 0},

		// exit 1: the sandbox cwd is not a git/jj repo (today's real behaviour).
		{"worktree_list", []string{"-p", goldensProfile, "worktree", "list"}, 1},

		{"config_show_effective_json", []string{"config", "show", "--effective", "--json"}, 0},

		{"profile_list", []string{"profile", "list"}, 0},
		{"profile_default_show", []string{"profile", "default"}, 0},

		{"inbox_export", []string{"-p", goldensProfile, "inbox", "export"}, 0},
		{"inbox_writer_status", []string{"-p", goldensProfile, "inbox", "writer-status"}, 0},

		{"hooks_status", []string{"hooks", "status"}, 0},

		{"watcher_list", []string{"-p", goldensProfile, "watcher", "list"}, 0},

		{"completion_bash", []string{"completion", "bash"}, 0},
		{"completion_zsh", []string{"completion", "zsh"}, 0},
		{"completion_fish", []string{"completion", "fish"}, 0},
	}
}

// helpSpecs is deliverable 1's universal --help coverage: every top-level
// command listed in printHelp()'s "Commands:" section, plus every documented
// subcommand from its per-area sections (Session/Fleet/MCP/Skill/Hook/Group/
// Conductor/Remote/Worktree/Config/Profile Commands). --help never touches
// tmux/ssh/network or mutates the store, so it is safe for every command
// including the ones in excludedCommands above.
func helpSpecs() []goldenSpec {
	topLevel := []string{
		"add", "launch", "accounts", "doctor", "health", "try", "list", "remove", "rename",
		"status", "session", "fleet", "mcp", "skill", "codex-hooks", "gemini-hooks",
		"hermes-hooks", "cursor-hooks", "tmux-hooks", "pi-hooks", "deepseek", "group",
		"worktree", "usage", "recall", "web", "remote", "conductor", "agents", "agent",
		"telegram-doctor", "profile", "update", "telemetry", "debug-dump", "migrate-paths",
		"uninstall", "completion", "costs", "config", "inbox", "feedback", "creds-refresh",
		"watcher", "openclaw", "system", "mcp-proxy", "hooks", "hook-handler", "codex-notify",
		"notify-daemon", "run-task",
	}

	subcommands := []string{
		"session start", "session stop", "session restart", "session fork", "session attach",
		"session show", "session viewers", "session context",
		"fleet status", "fleet recover",
		"mcp list", "mcp attached", "mcp attach", "mcp detach",
		"skill list", "skill attached", "skill attach", "skill detach", "skill source",
		"codex-hooks install", "codex-hooks uninstall", "codex-hooks status",
		"gemini-hooks install", "gemini-hooks uninstall", "gemini-hooks status",
		"hermes-hooks install", "hermes-hooks uninstall", "hermes-hooks status",
		"cursor-hooks install", "cursor-hooks uninstall", "cursor-hooks status",
		"tmux-hooks install", "tmux-hooks uninstall", "tmux-hooks status",
		"pi-hooks install", "pi-hooks uninstall", "pi-hooks status",
		"deepseek status", "deepseek profiles", "deepseek sessions",
		"group list", "group show", "group create", "group update", "group delete",
		"group move", "group change", "group reorder",
		"conductor setup", "conductor teardown", "conductor status", "conductor list",
		"remote add", "remote remove", "remote list", "remote sessions", "remote attach",
		"remote rename", "remote update",
		"worktree list", "worktree info", "worktree cleanup",
		"config show",
		"profile list", "profile create", "profile delete", "profile default",
		"inbox drain", "inbox export", "inbox writer-status", "inbox dead-letter",
		"hooks status",
		"watcher list", "watcher create", "watcher start", "watcher stop", "watcher status",
		"watcher test", "watcher routes",
		"agent adopt", "system stats",
		"costs sync", "costs summary", "costs recompute",
	}

	all := append(append([]string{}, topLevel...), subcommands...)
	specs := make([]goldenSpec, 0, len(all))
	for _, path := range all {
		parts := strings.Fields(path)
		args := append(append([]string{}, parts...), "--help")
		specs = append(specs, goldenSpec{
			name: "help_" + strings.ReplaceAll(path, " ", "_"),
			args: args,
			// --help usage text is printed on the success path for every
			// command above; a non-zero exit here means the command tree
			// changed shape (renamed/removed), which is exactly the kind of
			// drift these goldens exist to catch.
			wantExit: 0,
		})
	}
	return specs
}

// --- scrub list -------------------------------------------------------
//
// Documented in testdata/goldens/README.md. Keep the two in sync.

var scrubRules = []struct {
	pattern *regexp.Regexp
	repl    string
}{
	// RFC3339-ish timestamps, with or without fractional seconds/zone.
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})?`), "<TIMESTAMP>"},
	// "2026-09-22 12:00:00" style timestamps.
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`), "<TIMESTAMP>"},
	// Bare Unix epoch seconds/millis inside JSON number fields (10-13 digits).
	{regexp.MustCompile(`([:\[,]\s*)\d{10,13}(\s*[,\]}])`), "${1}<EPOCH>${2}"},
	// Agent Deck semantic version, dev build hash suffixes included.
	{regexp.MustCompile(`v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?`), "<VERSION>"},
	// Process IDs / port numbers rendered as "pid 12345" or ":54321".
	{regexp.MustCompile(`\bpid[ =]\d+\b`), "pid <PID>"},
	// The goldens binary itself: goldensBinary() builds to a fresh
	// os.MkdirTemp("", "ad-goldens-bin-") directory outside the sandbox HOME
	// (it must survive t.TempDir() cleanup across every sub-test), and some
	// commands (e.g. `hooks status`) echo argv[0]'s resolved path back.
	{regexp.MustCompile(`/[^\s"]*ad-goldens-bin-[0-9]+/agent-deck(\.exe)?`), "<AGENT_DECK_BINARY>"},
	// A live tmux session name: `internal/tmux.SessionPrefix` ("agentdeck_")
	// plus a title slug plus a random hex suffix, assigned fresh every time
	// a session actually starts/restarts in real tmux (storage-bytes
	// goldens only — the six never-started CLI-goldens fixtures keep their
	// literal seeded TmuxSession value and are never touched by this rule).
	{regexp.MustCompile(`agentdeck_[a-z0-9]+(-[a-z0-9]+)*_[0-9a-f]{6,}`), "<TMUX_SESSION>"},
}

// scrub normalizes volatile fields per testdata/goldens/README.md: timestamps,
// versions, and any path under the sandbox HOME (replaced with a fixed
// token so the golden is portable across machines/CI runners). IDs are not
// scrubbed here because the fixture uses fixed golden-sess-N / group paths,
// so real drift in ID *shape* still shows up as a diff.
func scrub(s, home string) string {
	if home != "" {
		s = strings.ReplaceAll(s, home, "<SANDBOX_HOME>")
		if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
			s = strings.ReplaceAll(s, real, "<SANDBOX_HOME>")
		}
	}
	for _, r := range scrubRules {
		s = r.pattern.ReplaceAllString(s, r.repl)
	}
	return s
}

// --- binary build (once per test process) -----------------------------

var (
	goldensBinOnce sync.Once
	goldensBinPath string
	goldensBinErr  error
)

func goldensBinary(t *testing.T) string {
	t.Helper()
	goldensBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ad-goldens-bin-")
		if err != nil {
			goldensBinErr = err
			return
		}
		bin := filepath.Join(dir, "agent-deck")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/agent-deck")
		cmd.Dir = goldensRepoRoot(t)
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			goldensBinErr = &buildFailure{err: buildErr, output: string(out)}
			return
		}
		goldensBinPath = bin
	})
	if goldensBinErr != nil {
		t.Fatalf("building goldens binary: %v", goldensBinErr)
	}
	return goldensBinPath
}

type buildFailure struct {
	err    error
	output string
}

func (b *buildFailure) Error() string { return b.err.Error() + "\n" + b.output }

func goldensRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// cmd/agent-deck -> repo root
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

// --- sandbox ------------------------------------------------------------

// goldensSandbox sets up the two-step sandbox HOME (see local-only-common.md)
// as env vars for exec.Command, seeds the store, and returns HOME plus the
// env slice to run the binary with.
func goldensSandbox(t *testing.T) (home string, env []string) {
	t.Helper()
	home = t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", goldensProfile)
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	t.Setenv("AGENT_DECK_TEST_HOME_ISOLATED", "1")

	seedGoldensStore(t, home)

	env = os.Environ()
	return home, env
}

func runGoldens(t *testing.T, bin string, env []string, args []string) (stdout string, exitCode int) {
	t.Helper()
	return runGoldensIn(t, bin, env, "", args)
}

// runGoldensIn runs the binary with cmd.Dir set to dir. A command whose
// output depends on the process's working directory (e.g. `config show`
// defaults its target to ".") would otherwise embed the real checkout path
// of whichever machine ran the test, breaking golden portability; every
// safeSpecs/helpSpecs invocation therefore runs from inside the sandbox
// HOME, which scrub() already normalizes to <SANDBOX_HOME>.
func runGoldensIn(t *testing.T, bin string, env []string, dir string, args []string) (stdout string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out // deliverable 1 asserts stdout; folding stderr in catches a
	// command that silently starts writing its output to the wrong stream.
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running %v: %v", args, err)
		}
	}
	return out.String(), exitCode
}

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(dir, "testdata", "goldens", name+".golden")
}

func assertGolden(t *testing.T, name, home, got string) {
	t.Helper()
	scrubbed := scrub(got, home)
	path := goldenPath(t, name)

	if shouldUpdateGoldens() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir goldens dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(scrubbed), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run with AGENTDECK_UPDATE_GOLDENS=1 to create it)", path, err)
	}
	if scrubbed != string(want) {
		t.Errorf("golden %s mismatch (scrubbed):\n--- want ---\n%s\n--- got ---\n%s", name, string(want), scrubbed)
	}
}

// TestCLIGoldens is deliverable 1: run every safe command (--json where
// supported) and every command's --help, compare against stored goldens.
func TestCLIGoldens(t *testing.T) {
	bin := goldensBinary(t)

	t.Run("safe", func(t *testing.T) {
		home, env := goldensSandbox(t)
		for _, spec := range safeSpecs() {
			spec := spec
			t.Run(spec.name, func(t *testing.T) {
				stdout, exit := runGoldensIn(t, bin, env, home, spec.args)
				if exit != spec.wantExit {
					t.Fatalf("exit code = %d, want %d; output:\n%s", exit, spec.wantExit, stdout)
				}
				assertGolden(t, spec.name, home, stdout)
			})
		}
	})

	t.Run("help", func(t *testing.T) {
		home, env := goldensSandbox(t)
		for _, spec := range helpSpecs() {
			spec := spec
			t.Run(spec.name, func(t *testing.T) {
				stdout, exit := runGoldensIn(t, bin, env, home, spec.args)
				if exit != spec.wantExit {
					t.Fatalf("exit code = %d, want %d; output:\n%s", exit, spec.wantExit, stdout)
				}
				assertGolden(t, spec.name, home, stdout)
			})
		}
	})
}

// TestCLIGoldensCoverageReport prints the N-covered / M-excluded accounting
// PROMPT.md deliverable 4 asks for. It never fails on its own; it exists so
// `go test -run TestCLIGoldensCoverageReport -v` gives a stable, greppable
// count for RESULTS.md instead of a hand count that drifts as specs change.
func TestCLIGoldensCoverageReport(t *testing.T) {
	safe := len(safeSpecs())
	help := len(helpSpecs())
	excluded := len(excludedCommands)
	t.Logf("coverage: %d safe (--json/table) commands, %d --help goldens, %d commands excluded from execution (still --help golden-tested)", safe, help, excluded)
	for _, e := range excludedCommands {
		t.Logf("excluded: %-28s %s", e.path, e.reason)
	}
}

// TestCLIGoldensExclusionsHaveNoSafeSpec is a self-check: an excluded command
// must never also appear in safeSpecs (that would silently execute something
// the exclusion list says is unsafe to execute).
func TestCLIGoldensExclusionsHaveNoSafeSpec(t *testing.T) {
	for _, spec := range safeSpecs() {
		full := strings.Join(spec.args, " ")
		for _, e := range excludedCommands {
			// Match on the excluded path appearing as the command's leading
			// words (after any -p/-g global flags), not as a substring
			// anywhere (e.g. "remote list" must not match "remote").
			if commandStartsWith(spec.args, e.path) {
				t.Errorf("safeSpecs %q executes excluded command %q (%s)", full, e.path, e.reason)
			}
		}
	}
}

func commandStartsWith(args []string, path string) bool {
	// Strip leading global flags (-p/-g/--select and their values).
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-p", "--profile", "-g", "--group", "--select":
			i += 2
			continue
		}
		break
	}
	want := strings.Fields(path)
	if len(args)-i < len(want) {
		return false
	}
	for j, w := range want {
		if args[i+j] != w {
			return false
		}
	}
	return true
}
