package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/sshcontrol"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// OpenContainerShell creates a tmux session running an interactive shell inside
// the sandbox container. Returns the tmux session name for attaching.
// Uses /bin/sh for portability (not all images have bash).
func (i *Instance) OpenContainerShell() (string, error) {
	if !i.IsSandboxed() {
		return "", fmt.Errorf("session %s is not sandboxed", i.ID)
	}
	if i.SandboxContainer == "" || !docker.IsManagedContainer(i.SandboxContainer) {
		return "", fmt.Errorf("no valid sandbox container for session %s", i.ID)
	}

	// Verify the container is still running before attempting docker exec.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ctr := docker.FromName(i.SandboxContainer)
	running, err := ctr.IsRunning(ctx)
	if err != nil {
		return "", fmt.Errorf("checking container %s: %w", i.SandboxContainer, err)
	}
	if !running {
		return "", fmt.Errorf("sandbox container %s is not running", i.SandboxContainer)
	}

	// Reuse the GenerateName prefix logic for consistency.
	tmuxName := "ad-term-" + docker.GenerateName(i.ID, i.Title)[len("agent-deck-"):]

	// Kill any existing terminal session to prevent orphans from repeated T presses.
	// Target the same socket the parent agent-deck instance lives on so the
	// terminal helper is visible to `tmux -L <socket> ls` and agent-deck's
	// own reap paths (issue #687).
	killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer killCancel()
	_ = tmux.ExecContext(killCtx, i.TmuxSocketName, "kill-session", "-t", tmuxName).Run()

	// Omit -w flag: the container's workdir was set during create (respects worktree path).
	// Pass the docker exec command as discrete tmux args to avoid shell interpolation of
	// the container name (defence-in-depth against state file tampering).
	//
	// #1694: -x/-y are mandatory on every detached new-session — without them
	// the pane is born at tmux's 80x24 default and the shell (plus anything the
	// user runs in it) paints its first frames clipped. See
	// tmux.InitialWindowSize.
	cols, rows := tmux.InitialWindowSize()
	newCtx, newCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer newCancel()
	newCmd := tmux.ExecContext(newCtx, i.TmuxSocketName,
		"new-session", "-d", "-s", tmuxName,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows),
		"docker", "exec", "-it", i.SandboxContainer, "/bin/sh",
	)
	// #1713: if this new-session is what starts the tmux server, the server
	// inherits this process's cwd — and agent-deck is frequently run from a
	// worktree that later gets deleted, after which tmux ignores every -c and
	// births all panes in that dead directory. Spawn from a directory that
	// cannot be unlinked. The pane's own cwd is irrelevant here: it runs
	// `docker exec`, whose working directory was fixed at container create.
	newCmd.Dir = tmux.SpawnBaseDir
	out, err := newCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating terminal session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return tmuxName, nil
}

// wrapForSSH wraps the command in an SSH invocation if the instance targets a remote host.
// Uses ControlMaster for connection multiplexing to avoid repeated handshakes.
func (i *Instance) wrapForSSH(command string) string {
	if !i.IsSSH() {
		return command
	}

	// Ensure the per-user ControlMaster socket directory exists and is private.
	controlPath := sshcontrol.ControlPath()

	// remoteCmd is the shell program the REMOTE shell will run. It is quoted
	// exactly once, by shellQuote below, when it becomes ssh's final argument.
	//
	// The command is spliced in verbatim: it is already a shell program (the
	// export prefix plus the harness invocation), not a literal to embed. It
	// used to be escaped here as well and then again by shellQuote, so every
	// single quote in the payload reached the remote shell malformed. That broke
	// EVERY ungrouped --ssh claude session with a --remote-path, because the
	// spawn prefix always emits `export AGENTDECK_RESOLVED_GROUP='';` — the
	// remote shell answered `unexpected EOF while looking for matching '` and
	// the session died in ~250ms, which is the symptom #1858 reports.
	//
	// RemoteCDPrefix (not a hand-rolled `cd '%s'`) is what makes the directory
	// the session RUNS in the same directory its identity says it is in: it
	// leaves a ~ prefix unquoted so the remote shell expands it against the
	// REMOTE user's home, and emits nothing at all for the spellings that
	// CanonicalRemotePath folds together ("", "~", "~/").
	remoteCmd := RemoteCDPrefix(i.SSHRemotePath) + command

	return fmt.Sprintf(
		"ssh -t -o ControlMaster=auto -o ControlPath=%s -o ControlPersist=600 %s %s",
		shellQuote(controlPath),
		shellQuote(i.SSHHost),
		shellQuote(remoteCmd),
	)
}

// wrapForSandbox wraps command in docker exec if the instance is sandboxed.
// Returns the wrapped command and the container name. The caller is responsible
// for persisting the container name to i.SandboxContainer.
func (i *Instance) wrapForSandbox(command string) (string, string, error) {
	if !i.IsSandboxed() {
		return command, "", nil
	}

	userCfg, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		sessionLog.Warn("load_user_config_for_sandbox", slog.String("error", cfgErr.Error()))
	}

	wrappedCmd, containerName, err := ensureSandboxContainer(i, userCfg, command)
	if err != nil {
		return "", "", err
	}
	return wrappedCmd, containerName, nil
}

// builtinAgentTools are the first-party agent CLIs agent-deck launches as a
// pane's initial process and whose clean exit (e.g. `/exit`) can fall back to
// an interactive shell when exit_to_shell is enabled (issue #1161).
var builtinAgentTools = map[string]bool{
	"claude":   true,
	"gemini":   true,
	"opencode": true,
	"codex":    true,
	"copilot":  true,
	"cursor":   true,
	"hermes":   true,
	"crush":    true,
	"muse":     true,
	// pi and omp (Oh My Pi) launch with the same `<env>; AGENTDECK_... pi
	// --continue ...` shape; they were left out of this list, so exit_to_shell
	// silently skipped them (issue #2556).
	"pi":  true,
	"omp": true,
}

// isBuiltinAgentTool reports whether tool is a first-party agent (or a custom
// tool wrapping claude/codex). Custom non-agent commands and "shell" are not
// agents and must never be exit-to-shell wrapped.
func isBuiltinAgentTool(tool string) bool {
	if builtinAgentTools[tool] {
		return true
	}
	return IsClaudeCompatible(tool) || IsCodexCompatible(tool)
}

// exitToShellEnabled resolves the exit-to-shell toggle for this instance.
// Per-session override (Instance.ExitToShell) wins; otherwise the global
// [shell] exit_to_shell config flag applies. Default is OFF (opt-in). #1161.
func (i *Instance) exitToShellEnabled() bool {
	if i.ExitToShell != nil {
		return *i.ExitToShell
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && cfg.Shell.GetExitToShell()
}

// wrapExitToShell rewrites a built-in agent's spawn command so the pane falls
// back to an interactive shell at the same cwd when the agent exits, restoring
// the pre-#503 exit→shell→resume workflow (issue #1161, Option A).
//
// The transform is:
//
//	<agent cmd>; exec "$SHELL" -i
//
// with the agent's own `exec ` launcher neutralised — claude execs itself for
// job control, which would replace the wrapping bash and prevent the trailing
// shell exec from ever running. Only the first `exec ` in command position
// (the launcher) is stripped; any later "exec " lives inside a shell-quoted
// startup-query suffix. An "exec " that is part of a longer word, such as the
// `find ... -exec ls` in a pi/omp fork command, is not a launcher and is kept
// (issue #2556). Agents that do not exec (gemini, codex, pi, …) are
// unaffected by the strip and simply get the suffix appended.
//
// No-op when the flag is off, the command is empty, the session is sandboxed
// (docker exec owns the in-container process), or the tool is not a built-in
// agent. Resume is unaffected: i.ClaudeSessionID is captured in Go before the
// command is built, so the `--session-id`/`--resume` id still targets the same
// session after the shell detour.
func (i *Instance) wrapExitToShell(command string) string {
	if command == "" || i.IsSandboxed() || !i.exitToShellEnabled() || !isBuiltinAgentTool(i.Tool) {
		return command
	}
	rewritten := command
	if loc := execLauncherRe.FindStringSubmatchIndex(command); loc != nil {
		rewritten = command[:loc[2]] + command[loc[3]:]
	}
	return rewritten + `; exec "$SHELL" -i`
}

// execLauncherRe matches the first `exec ` in shell command position: at the
// start of the command or after whitespace or a separator. Group 1 is the
// `exec ` to strip.
var execLauncherRe = regexp.MustCompile(`(?:^|[\s;&|(])(exec )`)

// launchShellEnabled returns whether the session should wrap agent commands
// with a shell invocation that loads startup files before launching the agent.
// Checks per-session override first, then falls back to global [shell].launch_shell config.
func (i *Instance) launchShellEnabled() bool {
	if i.LaunchShell != nil {
		return *i.LaunchShell
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && cfg.Shell.GetLaunchShell()
}

// wrapLaunchShell wraps the command with an interactive shell invocation so
// that environment variables from ~/.zshrc, ~/.bashrc, etc. are available to
// the agent process (issue #1218).
//
// The transform is:
//
//	$SHELL -il -c '<command>'
//
// where $SHELL is the user's configured shell (e.g. /bin/zsh, /bin/bash).
// For bash, ~/.bashrc is sourced explicitly before the command because
// interactive login bash does not read it automatically.
//
// This solves the issue where OpenCode MCP configs with {env:VAR} references
// fail when launched from the TUI because agent-deck spawns the agent directly
// without going through the user's interactive shell environment.
//
// No-op when the flag is off, the command is empty, the session is sandboxed
// (container already handles environment), or for shell tools (to avoid
// double-wrapping). SSH remote sessions are also excluded because the remote
// SSH invocation should handle the login shell setup.
func (i *Instance) wrapLaunchShell(command string) string {
	if command == "" || i.IsSandboxed() || !i.launchShellEnabled() {
		return command
	}
	// Don't wrap shell sessions or SSH sessions
	if i.Tool == "shell" || i.SSHHost != "" {
		return command
	}
	// Get the shell from environment, default to bash
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	// Escape single quotes in the command for safe shell quoting
	escaped := strings.ReplaceAll(command, "'", "'\"'\"'")
	if filepath.Base(shell) == "bash" {
		return fmt.Sprintf("%s -il -c 'if [ -f ~/.bashrc ]; then source ~/.bashrc; fi; %s'", shell, escaped)
	}
	return fmt.Sprintf("%s -il -c '%s'", shell, escaped)
}

// prepareCommand applies the full command wrapping chain: user wrapper → sandbox → ignore-suspend.
// Returns the wrapped command, the sandbox container name (empty if not sandboxed), and an error.
// All code paths that launch or respawn a tmux pane should use this instead of calling
// applyWrapper/wrapForSandbox/wrapIgnoreSuspend individually.
func (i *Instance) prepareCommand(cmd string) (string, string, error) {
	// Exit-to-shell wrap FIRST, on the bare agent command, so the agent's own
	// `exec ` launcher is still visible to neutralise and the trailing shell
	// exec stays the outermost statement before any user-wrapper / bash -c /
	// SSH layering. No-op unless opt-in for a built-in agent (issue #1161).
	cmd = i.wrapExitToShell(cmd)

	// PATH prelude next, so it sits inside every wrapper below (launch shell,
	// user wrapper's bash -c, sandbox) and the pane resolves the tool the way
	// a login shell would even when the tmux server was born under a
	// non-login SSH PATH (remote parity walk, g14). No-op when nothing is
	// missing. See spawn_path.go.
	cmd = i.wrapSpawnPath(cmd)

	// Launch-shell wrap SECOND, before user wrapper, so the interactive shell
	// loads its startup files and then executes the complete command (with
	// exit-to-shell suffix if enabled). This ensures env vars from ~/.zshrc,
	// ~/.bashrc, etc. are available to the agent and any trailing shell
	// (issue #1218). No-op unless opt-in.
	cmd = i.wrapLaunchShell(cmd)

	// Apply the user wrapper THIRD so that extra args folded into a
	// "{command} --flag1 --flag2" wrapper template become part of the string
	// that the bash -c wrap protects. Previously the order was reversed
	// (bash -c wrap then wrapper substitution), which produced
	// "bash -c '<cmd>' --flag1 --flag2" — bash treated --flag1/--flag2 as
	// positional parameters ($0, $1, …) and the child process never saw them.
	// See issue #601.
	wrapped, err := i.applyWrapper(cmd)
	if err != nil {
		return "", "", err
	}

	// Wrap the fully-substituted command under bash -c when a wrapper is
	// configured. This keeps shell metacharacters (&&, $(), inline env) in the
	// base command from leaking into the outer shell parse, and — critically —
	// keeps trailing wrapper-suffix flags INSIDE a single quoted argv so they
	// reach the child process intact.
	//
	// A SubcommandPassthrough Tool=="shell" instance needs the same wrap for
	// a different reason even though it has no configured wrapper:
	// buildShellPassthroughCommand injects bash/POSIX-only syntax ahead of
	// the subcommand — `export VAR='val' && `, `[ -f x ] && . x && `, inline
	// `VAR=val cmd` prefix assignments. Tool=="shell" instances are delivered
	// via tmux send-keys into the user's actual interactive login shell
	// (RunCommandAsInitialProcess is false for them), which is not
	// necessarily bash — a fish user's pane would receive a fish syntax
	// error ("Unsupported use of '='") instead of running the subcommand
	// (Claude review, PR #1821 HIGH #2). Explicitly invoking `bash -c '...'`
	// is itself valid syntax in any shell, so wrapping guarantees the
	// injected bash syntax is interpreted by bash regardless of the user's
	// default shell.
	if i.Account != "" {
		wrapped = "export AGENTDECK_ACCOUNT=" + shellescape.Quote(i.Account) + "; " + wrapped
	}
	//
	// An SSH payload gets the same wrap for the reason the shell case does:
	// it runs in the remote user's LOGIN shell, which may be fish or csh, and
	// the payload is POSIX/bash syntax (spawn PATH prelude, env exports, the
	// identity prefix's ${X:-} / { ...; } / $$). Only an Account or wrapper
	// used to put it under bash; now every SSH payload is.
	if i.hasEffectiveWrapper() || i.Account != "" || (i.Tool == "shell" && i.SubcommandPassthrough) || i.IsSSH() {
		escaped := strings.ReplaceAll(wrapped, "'", "'\"'\"'")
		wrapped = fmt.Sprintf("bash -c '%s'", escaped)
	}
	wrapped = i.wrapForSSH(wrapped)
	wrapped, containerName, err := i.wrapForSandbox(wrapped)
	if err != nil {
		return "", "", err
	}
	// Only disable Ctrl+Z suspend for sandboxed sessions where the command
	// runs as the pane's initial process (no interactive shell for job control).
	// Non-sandbox sessions use send-keys into an interactive shell, so Ctrl+Z
	// naturally suspends the process and the user can `fg` to resume.
	if wrapped != "" && i.IsSandboxed() {
		wrapped = wrapIgnoreSuspend(wrapped)
	}
	return wrapped, containerName, nil
}

// terminalEnvVars are always passed through to containers for proper UI/theming.
var terminalEnvVars = []string{"TERM", "COLORTERM", "FORCE_COLOR", "NO_COLOR", "COLORFGBG"}

// collectDockerEnvVars returns host environment variables to forward to containers.
// Each call reads fresh values from the host environment via os.LookupEnv so that
// changes between session starts (e.g. updated TERM) are picked up immediately.
// Terminal-related variables (TERM, COLORTERM, FORCE_COLOR, NO_COLOR, COLORFGBG) are always
// included when set. Additional names from DockerSettings.Environment are appended.
func collectDockerEnvVars(names []string) map[string]string {
	env := make(map[string]string, len(terminalEnvVars)+len(names))
	for _, name := range terminalEnvVars {
		if val, ok := os.LookupEnv(name); ok {
			env[name] = val
			continue
		}
		if name == "COLORFGBG" {
			env[name] = ThemeColorFGBG()
		}
	}
	for _, name := range names {
		if val, ok := os.LookupEnv(name); ok {
			env[name] = val
		}
	}
	return env
}

// ensureSandboxContainer creates and starts the Docker container if needed,
// then returns the tool command wrapped in "docker exec" and the container name.
// The userCfg parameter avoids a redundant LoadUserConfig call — the caller
// (wrapForSandbox) already loaded it.
func ensureSandboxContainer(inst *Instance, userCfg *UserConfig, toolCommand string) (string, string, error) {
	// Use a bounded context to prevent indefinite hangs if Docker is unresponsive.
	// Image pulls may take longer, but CheckAvailability/Exists/Create/Start should be fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := docker.CheckAvailability(ctx); err != nil {
		return "", "", fmt.Errorf("sandbox unavailable: %w", err)
	}

	if err := docker.EnsureImage(ctx, inst.Sandbox.Image); err != nil {
		return "", "", fmt.Errorf("ensuring sandbox image: %w", err)
	}

	containerName := docker.GenerateName(inst.ID, inst.Title)
	ctr := docker.NewContainer(containerName, inst.Sandbox.Image)

	homeDir, homeErr := os.UserHomeDir()
	if homeErr != nil {
		sessionLog.Warn("user_home_dir", slog.String("error", homeErr.Error()))
	}

	// Skip agent config sync when home directory is unavailable — RefreshAgentConfigs
	// would produce broken paths rooted at "/" with an empty homeDir.
	var bindMounts []docker.VolumeMount
	var homeMounts []docker.VolumeMount
	if homeDir != "" {
		var syncOpts []docker.SyncOption
		if userCfg != nil && userCfg.Docker.SeedCredentialsFromKeychain {
			syncOpts = append(syncOpts, docker.WithKeychainSeed())
		}
		bindMounts, homeMounts = docker.RefreshAgentConfigs(homeDir, "", syncOpts...)
		if IsCodexCompatible(inst.Tool) {
			if err := PreAcceptCodexSandboxWorkspaceTrust(homeDir); err != nil {
				sessionLog.Warn("codex_sandbox_preaccept_trust_failed",
					slog.String("instance_id", inst.ID),
					slog.String("error", err.Error()))
			}
		}
	}

	if err := ensureContainerRunning(ctx, inst, ctr, userCfg, homeDir, bindMounts, homeMounts); err != nil {
		return "", "", err
	}

	return buildExecCommand(ctr, userCfg, toolCommand), containerName, nil
}

// ensureContainerRunning creates and starts the container if it doesn't exist or is stopped.
func ensureContainerRunning(
	ctx context.Context,
	inst *Instance,
	ctr *docker.Container,
	userCfg *UserConfig,
	homeDir string,
	bindMounts []docker.VolumeMount,
	homeMounts []docker.VolumeMount,
) error {
	exists, err := ctr.Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking sandbox container: %w", err)
	}

	if !exists {
		cfg := buildSandboxConfig(inst, userCfg, homeDir, bindMounts, homeMounts)
		if _, createErr := ctr.Create(ctx, cfg); createErr != nil {
			return fmt.Errorf("creating sandbox container: %w", createErr)
		}
	}

	running, err := ctr.IsRunning(ctx)
	if err != nil {
		return fmt.Errorf("checking sandbox container status: %w", err)
	}
	if !running {
		if startErr := ctr.Start(ctx); startErr != nil {
			return fmt.Errorf("starting sandbox container: %w", startErr)
		}
	}

	// Migration guard: older containers were created with root-owned tmpfs mounts
	// for /root/.npm and /root/.cache. With --user uid:gid this causes plugin
	// bootstrap failures (EACCES mkdir '/root/.npm/_cacache'). Recreate the
	// container once if those paths are not writable.
	cacheWritable := sandboxCacheDirsWritable(ctx, ctr)
	tmpExecutable := sandboxTmpExecutable(ctx, ctr)
	if !cacheWritable || !tmpExecutable {
		sessionLog.Warn(
			"sandbox_recreating_for_runtime_compat",
			slog.Bool("cache_writable", cacheWritable),
			slog.Bool("tmp_executable", tmpExecutable),
		)
		if rmErr := ctr.Remove(ctx, true); rmErr != nil {
			return fmt.Errorf("removing incompatible sandbox container: %w", rmErr)
		}
		cfg := buildSandboxConfig(inst, userCfg, homeDir, bindMounts, homeMounts)
		if _, createErr := ctr.Create(ctx, cfg); createErr != nil {
			return fmt.Errorf("recreating sandbox container: %w", createErr)
		}
		if startErr := ctr.Start(ctx); startErr != nil {
			return fmt.Errorf("starting recreated sandbox container: %w", startErr)
		}
	}

	return nil
}

func sandboxCacheDirsWritable(ctx context.Context, ctr *docker.Container) bool {
	return sandboxExecProbe(ctx, ctr, "test -w /root/.npm && test -w /root/.cache")
}

func sandboxTmpExecutable(ctx context.Context, ctr *docker.Container) bool {
	probe := `f=/tmp/.agent_deck_exec_probe.sh; printf '#!/bin/sh\nexit 0\n' > "$f" && chmod +x "$f" && "$f" >/dev/null 2>&1 && rm -f "$f"`
	return sandboxExecProbe(ctx, ctr, probe)
}

func sandboxExecProbe(ctx context.Context, ctr *docker.Container, script string) bool {
	prefix := ctr.ExecPrefixNonInteractive()
	args := append(prefix[1:], "bash", "-lc", script)
	// #nosec G204 -- prefix comes from docker.Container.ExecPrefixNonInteractive
	// (returns ["docker", "exec", containerName]); script is a hardcoded probe
	// snippet from callers above. No external input.
	_, err := exec.CommandContext(ctx, prefix[0], args...).CombinedOutput()
	return err == nil
}

// buildSandboxConfig assembles the ContainerConfig from session and user settings.
func buildSandboxConfig(
	inst *Instance,
	userCfg *UserConfig,
	homeDir string,
	bindMounts []docker.VolumeMount,
	homeMounts []docker.VolumeMount,
) *docker.ContainerConfig {
	var cpuLimit, memLimit string
	if inst.Sandbox.CPULimit != nil {
		cpuLimit = *inst.Sandbox.CPULimit
	}
	if inst.Sandbox.MemoryLimit != nil {
		memLimit = *inst.Sandbox.MemoryLimit
	}
	if cpuLimit == "" && userCfg != nil {
		cpuLimit = userCfg.Docker.CPULimit
	}
	if memLimit == "" && userCfg != nil {
		memLimit = userCfg.Docker.MemoryLimit
	}

	configOpts := []docker.ContainerConfigOption{
		docker.WithCPULimit(cpuLimit),
		docker.WithMemoryLimit(memLimit),
		docker.WithAgentConfigs(bindMounts, homeMounts),
	}

	// Bridge in-container hook-handler status writes to a PER-INSTANCE host dir.
	// The container's own hooks path sits on the read-only rootfs, so without
	// this mount Stop/transition events from sandboxed sessions are lost. The
	// dir is scoped to this instance (…/hooks/sandbox/<id>) rather than the
	// global fleet-wide hooks dir. Three properties keep this safe: (1) only this
	// instance's subdir is mounted, so a compromised container can read/write
	// files ONLY inside its own subdir — it can never see siblings' or the
	// conductor's status; (2) the host StatusFileWatcher keys a scoped file
	// by its OWNING SUBDIR and ingests only <id>.json, so a container cannot
	// forge a sibling's transition (or inject a done_summary into the conductor)
	// by naming a file after a victim inside its own subdir; and (3) every host
	// read of a status file is no-follow (O_NOFOLLOW, plus Lstat on the scoped
	// path) and size-bounded, so the container cannot symlink its own <id>.json
	// at a sibling/host file or /dev/zero, nor write a huge <id>.json, to read
	// host files or DoS the shared notify-daemon. The host read path
	// (readHookStatusFile / hookStatusFilePath) also builds the path from the
	// requested <id>, so it cannot be cross-attributed either.
	if hooksDir := GetHooksDir(); hooksDir != "" {
		perInstanceDir := filepath.Join(hooksDir, "sandbox", inst.ID)
		if mkErr := os.MkdirAll(perInstanceDir, 0o700); mkErr == nil {
			configOpts = append(configOpts, docker.WithHooksDir(perInstanceDir))
		} else {
			// Don't fail the spawn, but surface it: without the scoped hooks dir
			// the bridge mount is silently skipped, leaving the host watcher blind
			// to this sandboxed session — the exact problem this bridge solves.
			sessionLog.Warn("scoped_hooks_dir_create_failed",
				slog.String("instance_id", inst.ID),
				slog.String("dir", perInstanceDir),
				slog.String("error", mkErr.Error()))
		}
	}

	// Note: Docker.Environment names (e.g. TERM) are NOT forwarded at create time.
	// They are forwarded at exec time via buildExecCommand with fresh host values.
	// Only Docker.EnvironmentValues (static key=value pairs) are baked into the container.

	if homeDir != "" {
		gitconfigPath := filepath.Join(homeDir, ".gitconfig")
		if _, statErr := os.Stat(gitconfigPath); statErr == nil {
			configOpts = append(configOpts, docker.WithGitConfig(gitconfigPath))
		}
	}

	if userCfg != nil && userCfg.Docker.MountSSH && homeDir != "" {
		sshPath := filepath.Join(homeDir, ".ssh")
		if _, statErr := os.Stat(sshPath); statErr == nil {
			configOpts = append(configOpts, docker.WithSSH(sshPath))
		}
	}

	if userCfg != nil && len(userCfg.Docker.VolumeIgnores) > 0 {
		configOpts = append(configOpts, docker.WithVolumeIgnores(userCfg.Docker.VolumeIgnores))
	}

	if userCfg != nil && len(userCfg.Docker.ExtraVolumes) > 0 {
		configOpts = append(configOpts, docker.WithExtraVolumes(userCfg.Docker.ExtraVolumes))
	}

	if userCfg != nil && len(userCfg.Docker.EnvironmentValues) > 0 {
		configOpts = append(configOpts, docker.WithEnvironment(userCfg.Docker.EnvironmentValues))
	}

	// Multi-repo: mount each path under /workspace/<dirname> instead of single project mount.
	if inst.MultiRepoEnabled {
		configOpts = append(configOpts, docker.WithMultiRepoPaths(inst.AllProjectPaths()))
	}

	return docker.NewContainerConfig(inst.ProjectPath, configOpts...)
}

// buildExecCommand returns a shell-safe "docker exec ... bash -c toolCommand" string.
// The two-layer bash -c architecture:
//  1. Inner: docker exec ... bash -c <toolCommand> — runs the agent command inside
//     the container, properly shell-quoted by ShellJoinArgs.
//  2. Outer: wrapIgnoreSuspend wraps the entire string in bash -c with stty susp undef,
//     which tmux then delivers via its implicit /bin/sh -c.
//
// This prevents shell injection: toolCommand (which may contain user-controlled text
// like session IDs) is passed as a single quoted argument to bash -c inside the container.
func buildExecCommand(ctr *docker.Container, userCfg *UserConfig, toolCommand string) string {
	// Always collect terminal env vars; append user-configured env var names.
	var userNames []string
	if userCfg != nil {
		userNames = userCfg.Docker.Environment
	}
	runtimeEnv := collectDockerEnvVars(userNames)

	var prefix []string
	if len(runtimeEnv) > 0 {
		prefix = ctr.ExecPrefixWithEnv(runtimeEnv)
	} else {
		prefix = ctr.ExecPrefix()
	}
	// Wrap toolCommand in bash -c inside the container so it is passed as a single
	// shell-quoted argument, preventing injection of shell metacharacters.
	return docker.ShellJoinArgs(append(prefix, "bash", "-c", toolCommand))
}

// generateUUID generates a cryptographically random UUID v4 as a lowercase string.
// Pre-generating in Go (instead of using shell $(uuidgen)) ensures the ID is immediately
// known to the Instance and avoids Docker-sandbox failures where uuidgen is unavailable.
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: timestamp-based placeholder; unique but not a valid UUID v4.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits (RFC 4122)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateID generates a unique session ID
// GenerateID creates a unique session identifier.
func GenerateID() string {
	return fmt.Sprintf("%s-%d", randomString(8), time.Now().Unix())
}

// randomString generates a random hex string of specified length
func randomString(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// wrapIgnoreSuspend wraps cmd in a bash -c invocation that disables CTRL+Z
// suspension before running the command. This is the sole bash -c layer
// in the command chain — ensureSandboxContainer returns a plain docker exec
// string, matching AoE's single-wrapper pattern.
//
// No `exec` is used: compound commands (&&, ||, pipes) must remain valid
// when passed through Restart() resume paths. The extra shell process is
// negligible overhead.
func wrapIgnoreSuspend(cmd string) string {
	// Escape single quotes for safe embedding inside a single-quoted string.
	// Pattern: end quote, add double-quoted literal quote, restart quote.
	// Example: it's -> it'"'"'s -> bash sees: it's.
	escaped := strings.ReplaceAll(cmd, `'`, `'"'"'`)
	return "bash -c 'stty susp undef; " + escaped + "'"
}
