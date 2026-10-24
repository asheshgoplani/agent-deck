package tmux

import (
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

func sanitizeSystemdUnitComponent(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
		if out == "" {
			return "session"
		}
	}
	return out
}

// bashCWrap returns the given command wrapped in `bash -c '...'` with
// single quotes safely escaped using the POSIX shell quote-break pattern. The result
// is a single shell word that can be passed to any `sh -c` invocation
// (e.g. tmux's default shell-command delivery) and will always be
// executed under bash, giving consistent semantics regardless of the
// user's login shell.
func bashCWrap(command string) string {
	escaped := strings.ReplaceAll(command, `'`, `'\''`)
	return bashCPrefix + escaped + "'"
}

const (
	bashBinary  = "bash"
	bashCPrefix = "bash -c '"
)

// cwdAssertCommand prefixes cmd with a `cd -- <dir> &&` so the pane's own
// process asserts its working directory itself instead of trusting tmux's
// `-c` alone (#2214). On some tmux/OS builds, once the tmux SERVER's own cwd
// has been unlinked (e.g. it was started from a worktree later removed), the
// server stops honouring `-c` for every new pane and births it in that dead
// directory instead — see TestGroundTruth_PoisonedServerIgnoresDashCForNewPanes.
// The shell builtin `cd` does not depend on the process's inherited cwd being
// valid (it operates on the filesystem via the given path directly), so this
// lands the pane in the right place regardless of what state the server's own
// cwd is in. This makes the server's cwd irrelevant to where the pane's real
// process runs, for every spawn, on the first attempt — not a fallback that
// only kicks in after detecting a failure.
//
// dir is shell-quoted via shellescape.Quote so directories containing spaces
// or shell metacharacters are embedded safely. cmd is appended verbatim: it is
// already a complete, valid shell command line built by the caller (including
// any leading "exec " — cd is a builtin, so a following exec still runs
// inside the same shell and correctly replaces its process image).
func cwdAssertCommand(dir, cmd string) string {
	prefix := "cd -- " + shellescape.Quote(dir)
	if cmd == "" {
		return prefix
	}
	return prefix + " && " + cmd
}

// LaunchMode enumerates the resolved spawn form used by startCommandSpec.
// The string values are stable and used in logs + fallback diagnostics.
const (
	launchModeDirect  = "direct"
	launchModeScope   = "scope"
	launchModeService = "service"
)

// resolveLaunchMode returns one of launchModeDirect / launchModeScope /
// launchModeService based on LaunchAs (primary) and LaunchInUserScope
// (legacy fallback for empty LaunchAs). Unknown LaunchAs values fall
// through to the legacy LaunchInUserScope path — callers populate
// LaunchAs from TmuxSettings.GetLaunchAs which already canonicalises.
func (s *Session) resolveLaunchMode() string {
	switch strings.ToLower(strings.TrimSpace(s.LaunchAs)) {
	case "service":
		return launchModeService
	case "scope":
		return launchModeScope
	case "direct":
		return launchModeDirect
	case "auto":
		// Prefer service when systemd-user-manager is available;
		// otherwise fall through to direct. isSystemdUserScopeAvailable
		// already probes `systemd-run --user --version` which is
		// exactly the precondition for the service spawn too.
		if isSystemdUserScopeAvailable() {
			return launchModeService
		}
		return launchModeDirect
	case "":
		if s.LaunchInUserScope {
			return launchModeScope
		}
		return launchModeDirect
	default:
		// Unknown value — log once, fall through to legacy. Valid
		// values are enforced upstream in TmuxSettings.GetLaunchAs, so
		// reaching this branch means a caller populated LaunchAs from
		// somewhere other than GetLaunchAs (e.g. a test).
		statusLog.Warn("tmux_launch_as_invalid",
			slog.String("value", s.LaunchAs),
			slog.String("resolved", "legacy"))
		if s.LaunchInUserScope {
			return launchModeScope
		}
		return launchModeDirect
	}
}

// isSystemdUserScopeAvailable probes whether `systemd-run --user` is
// operational. Defined in internal/session/userconfig.go — we avoid
// import cycles by taking the probe result via a swappable seam. Tests
// can override this via the systemdUserRunProbe variable below.
var systemdUserRunProbe = func() bool {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	return commandRun(exec.Command("systemd-run", "--user", "--version")) == nil
}

func isSystemdUserScopeAvailable() bool {
	return systemdUserRunProbe()
}

func (s *Session) startCommandSpec(workDir, command string) (string, []string) {
	startWithInitialProcess := command != "" && s.RunCommandAsInitialProcess
	// Socket isolation (issue #687, v1.7.50): prepend `-L <name>` to the
	// bare tmux args so the new-session spawn and every subsequent lookup
	// target the same isolated server. Without this, the session would be
	// minted on the default server while later Session.tmuxCmd calls —
	// which DO carry -L — would probe the isolated server and find
	// nothing. Empty SocketName emits no -L at all, so the spawn still lands
	// on the user's default server exactly as it did pre-v1.7.50.
	//
	// #1694: -x/-y birth the window at the terminal agent-deck runs on
	// (generous fallback when there is no TTY) so the tool's FIRST paint is
	// already full-width. Without them tmux uses its 80x24 default and a TUI
	// that fixes its layout on frame one stays clipped forever — see
	// InitialWindowSize. Appended AFTER -c so the argv prefix that callers and
	// fallback helpers key on is unchanged.
	// buildInnerTmuxArgs also prepends tmux's global -u (#1867). That is
	// unconditional and independent of the socket: it forces the CLIENT to
	// UTF-8 so nothing agent-deck later reads back is "_"-mangled. It is
	// spliced INSIDE the inner tmux argv here, after the literal "tmux" that
	// systemd-run execs — a global flag placed before "tmux" would be
	// systemd-run's, not tmux's.
	cols, rows := InitialWindowSize()
	tmuxArgs := buildInnerTmuxArgs(s.SocketName, "new-session", "-d", "-s", s.Name, "-c", workDir,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
	tmuxArgs = append(tmuxArgs, launchEnvArgs(s.LaunchEnv)...)
	if startWithInitialProcess {
		// Deliver the pane command as SEPARATE argv tokens (bash, -c, command)
		// rather than a single shell-quoted string. This is the crux of the
		// #1567 / #1580 fix.
		//
		// tmux treats a single trailing string command as a shell-command and
		// delivers it through the server's default-shell: `$SHELL -c "…"`.
		// That implicit wrap kills fast-TTY-acquiring tools within
		// milliseconds — Hermes Agent dies <100ms (#1567), `npx codex` dies
		// instantly (#1580) — because the tool is no longer the pane leader
		// tmux execvp()'d, and $SHELL's controlling-terminal handoff differs.
		//
		// Passing separate argv tokens makes tmux execvp() `bash` directly as
		// the pane's initial process (the default-shell is never involved),
		// and bash then runs the command with a well-defined TTY, exactly like
		// the surviving `tmux new-session -d bash -c hermes` repro. bash is
		// kept as the interpreter for fish/zsh compatibility (#526).
		//
		// Payloads already shaped `bash -c '…'` (e.g. from wrapIgnoreSuspend)
		// pass through as the command argument verbatim — bash -c "bash -c '…'"
		// tail-exec's the inner bash, so no extra lingering process and no
		// re-escaping of the nested single quotes.
		//
		// #2214: cwdAssertCommand prefixes a `cd -- workDir &&` so the pane's
		// actual process asserts its own directory rather than trusting the
		// `-c workDir` above alone — see cwdAssertCommand's doc comment.
		tmuxArgs = append(tmuxArgs, bashBinary, "-c", cwdAssertCommand(workDir, command))
		// A one-shot can exit before Start's later option pass reaches tmux.
		// Set remain-on-exit in this command queue so tmux retains its output
		// even when the initial process finishes immediately.
		if s.OptionOverrides["remain-on-exit"] == "on" {
			tmuxArgs = append(tmuxArgs, ";", "set-option", "-t", s.Name, "remain-on-exit", "on")
		}
	}

	unitBase := serviceUnitBase(s.Name)

	switch s.resolveLaunchMode() {
	case launchModeService:
		// Type=forking is the ONLY viable type for tmux: tmux new-session
		// -d daemonizes, so Type=simple would see ExecStart exit 0 and
		// mark the service inactive immediately, defeating Restart=.
		// Empirically validated in the v1.7.21 pre-check
		// (see .planning/v1721-scope-to-service/PLAN.md): Type=forking +
		// kill -9 tmux → NRestarts=1 within 4s; Type=simple → NRestarts=0.
		//
		// We DO NOT use --collect here: --collect unloads the unit once
		// inactive, which would race with Restart= semantics.
		svcArgs := []string{
			"--user", "--unit", unitBase + ".service", "--quiet",
			"--property=Type=forking",
			"--property=Restart=on-failure",
			"--property=RestartSec=5s",
			"--property=StartLimitBurst=10",
			"--property=StartLimitIntervalSec=60",
			// A tmux server is shared by every session on its socket. A
			// per-session unit must never kill that shared server (and its
			// sibling panes) when the unit is stopped.
			"--property=KillMode=none",
			"--property=TimeoutStopSec=15s",
			"tmux",
		}
		svcArgs = append(svcArgs, tmuxArgs...)
		return "systemd-run", svcArgs

	case launchModeScope:
		// The scope remains the SSH/logout-isolation compatibility path, but
		// its cgroup can host the shared tmux server. KillMode=none prevents
		// stopping this per-session scope from taking that server (and all
		// sibling sessions) with it.
		scopeArgs := []string{
			"--user", "--scope", "--quiet", "--collect", "--unit", unitBase,
			"--property=KillMode=none", "tmux",
		}
		scopeArgs = append(scopeArgs, tmuxArgs...)
		return "systemd-run", scopeArgs

	default:
		return "tmux", tmuxArgs
	}
}

// buildScopeArgsFromTmuxArgs reconstructs scope-mode systemd-run argv
// from the bare tmux args. Used by the three-tier fallback in Start()
// when service-mode spawn fails and we retry with scope mode before
// falling all the way back to direct tmux.
func buildScopeArgsFromTmuxArgs(sessionName string, tmuxArgs []string) []string {
	unitBase := serviceUnitBase(sessionName)
	// Keep the service → scope fallback just as safe as an explicitly
	// selected scope. Otherwise a transient service failure would silently
	// reintroduce a per-session cgroup that can kill a shared server.
	scopeArgs := []string{"--user", "--scope", "--quiet", "--collect", "--unit", unitBase,
		"--property=KillMode=none", "tmux"}
	return append(scopeArgs, tmuxArgs...)
}

// wasServiceModeArgs detects whether systemd-run args produced by
// startCommandSpec are for service mode (contains --unit X.service).
// Used by the fallback chain to pick human-readable diagnostic labels
// and decide whether to attempt the scope-mode retry tier.
func wasServiceModeArgs(args []string) bool {
	for i, a := range args {
		if a == "--unit" && i+1 < len(args) && strings.HasSuffix(args[i+1], ".service") {
			return true
		}
	}
	return false
}

// wasScopeModeArgs detects whether systemd-run args are for scope mode
// (contains --scope). Symmetric helper used by the fallback chain.
func wasScopeModeArgs(args []string) bool {
	for _, a := range args {
		if a == "--scope" {
			return true
		}
	}
	return false
}

// Service-unit teardown lives in service_unit_ownership.go: it must go
// through StopServiceUnitOwned, which proves exclusive ownership of the
// unit before stopping it. The unconditional StopServiceUnit(sessionName)
// this file used to export was removed in issue #1721 — on the shared
// default socket it could stop the tmux server (and every sibling session
// in its cgroup) that one deleted session merely happened to name.

// stripSystemdRunPrefix removes the leading systemd-run flags from args
// produced by startCommandSpec (either scope-mode or service-mode form)
// and returns the bare tmux args. Scans for the first bare "tmux" token
// which, in both shapes, is the command argument to systemd-run —
// everything after it is tmux argv.
//
// Returns args unchanged if no "tmux" token is found (shape mismatch),
// preserving the defensive-against-refactors behavior of the original.
//
// Scope-mode shape (PR #467):
//
//	[0]   "--user"
//	[1]   "--scope"
//	[2]   "--quiet"
//	[3]   "--collect"
//	[4]   "--unit"
//	[5]   "<unit name>"
//	[6]   "tmux"
//	[7..] tmux args
//
// Service-mode shape (v1.7.21+):
//
//	[0]   "--user"
//	[1]   "--unit"
//	[2]   "<unit name>.service"
//	[3]   "--quiet"
//	[4..10] "--property=..." (variable count)
//	[11]  "tmux"
//	[12..] tmux args
//
// A "--property=..." value never equals "tmux" as a whole arg (they are
// single KEY=VALUE tokens), so the scan is unambiguous.
func stripSystemdRunPrefix(args []string) []string {
	for i, a := range args {
		if a == "tmux" {
			return args[i+1:]
		}
	}
	return args
}
