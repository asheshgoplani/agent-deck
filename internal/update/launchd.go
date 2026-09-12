package update

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// AutoupdateLabel is the launchd label of the daily update timer. It is
// always skipped by the post-install hygiene: its program is /bin/sh (see
// LaunchdPlist) so it is immune to the BTM hazard, and booting it out from
// inside an unattended run would kill that very run.
const AutoupdateLabel = "com.agentdeck.autoupdate"

// agentdeckLabelPrefix selects the launch agents the hygiene may touch.
const agentdeckLabelPrefix = "com.agentdeck."

// RebootstrapOptions configures RebootstrapLaunchAgents and
// PreflightLaunchctl. Zero values resolve to the real host (runtime.GOOS,
// os.Executable, ~/Library/LaunchAgents, os.Getuid, ExecRunner, time.Sleep,
// os.Stdout); tests set every field.
type RebootstrapOptions struct {
	GOOS            string
	ExePath         string
	LaunchAgentsDir string
	UID             int
	Runner          Runner
	Sleep           func(time.Duration)
	Out             io.Writer
	Logger          *slog.Logger
	// VerifyTimeout bounds the "state = running" poll (default 10s, 500ms steps).
	VerifyTimeout time.Duration
	// now is the clock the verify poll reads; tests pair it with Sleep to
	// advance a fake clock instead of waiting.
	now func() time.Time
}

// RebootstrapResult reports what the hygiene did.
type RebootstrapResult struct {
	// Restarted lists labels that were booted out and bootstrapped again.
	Restarted []string
	// Skipped maps label (or file name when unparsable) to the reason.
	Skipped map[string]string
}

// AgentRestartError is returned when an agent did not come back after the
// binary was replaced. It carries the exact commands to run by hand.
type AgentRestartError struct {
	Label  string
	Cause  error
	Repair []string // shell lines
}

func (e *AgentRestartError) Error() string {
	return fmt.Sprintf("%s did not come back: %v; run: %s", e.Label, e.Cause, strings.Join(e.Repair, "; "))
}

func (e *AgentRestartError) Unwrap() error { return e.Cause }

func (o *RebootstrapOptions) fill() error {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Logger == nil {
		o.Logger = updateLog
	}
	if o.VerifyTimeout <= 0 {
		o.VerifyTimeout = 10 * time.Second
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.UID == 0 {
		o.UID = os.Getuid()
	}
	if o.ExePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		o.ExePath = exe
	}
	if o.LaunchAgentsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home: %w", err)
		}
		o.LaunchAgentsDir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return nil
}

// launchctlDomain is the per-user GUI domain the agents live in.
func launchctlDomain(uid int) string { return fmt.Sprintf("gui/%d", uid) }

// launchctlTarget is the domain/label form launchctl bootout and print take.
func launchctlTarget(uid int, label string) string {
	return launchctlDomain(uid) + "/" + label
}

// PreflightLaunchctl checks that the launchd hygiene can run at all:
// launchctl must be on PATH and the user's gui domain must be reachable.
// Not darwin: nil. The unattended flow refuses to install when this fails;
// interactive flows only warn.
func PreflightLaunchctl(opts RebootstrapOptions) error {
	if err := opts.fill(); err != nil {
		return err
	}
	if opts.GOOS != "darwin" {
		return nil
	}
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("launchctl not found on PATH: %w", err)
	}
	argv := []string{"launchctl", "print", launchctlDomain(opts.UID)}
	out, err := opts.Runner.Run(argv...)
	if err != nil {
		opts.Logger.Warn("launchctl_preflight_failed", slog.String("cmd", ShellQuote(argv)), slog.String("out", strings.TrimSpace(out)))
		return fmt.Errorf("`%s` failed (%v): %s", ShellQuote(argv), err, firstLine(out))
	}
	opts.Logger.Info("launchctl_preflight_ok", slog.String("domain", launchctlDomain(opts.UID)))
	return nil
}

// RebootstrapLaunchAgents boots out and bootstraps again every launch agent
// whose program is the replaced executable, so it picks up the new code
// identity instead of crash-looping with EX_CONFIG (2026-07-30 incident:
// macOS BTM ties a user agent's identity to the file at its program path, so
// after that file is replaced launchd refuses to start it until the agent is
// re-registered).
//
// Only ~/Library/LaunchAgents/com.agentdeck.*.plist files are considered, and
// of those only agents whose Program or ProgramArguments[0] equals ExePath
// (raw or after resolving symlinks on both sides). com.agentdeck.autoupdate
// is always skipped. Not darwin: no-op.
func RebootstrapLaunchAgents(opts RebootstrapOptions) (RebootstrapResult, error) {
	res := RebootstrapResult{Skipped: map[string]string{}}
	if err := opts.fill(); err != nil {
		return res, err
	}
	if opts.GOOS != "darwin" {
		return res, nil
	}
	log := opts.Logger

	entries, err := filepath.Glob(filepath.Join(opts.LaunchAgentsDir, "*.plist"))
	if err != nil {
		return res, err
	}
	sort.Strings(entries)

	exeRaw := opts.ExePath
	exeReal := resolveOrSelf(exeRaw)

	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			res.Skipped[filepath.Base(path)] = "unreadable: " + err.Error()
			continue
		}
		agent, err := ParseLaunchAgentPlist(data)
		if err != nil {
			// Not ours to judge; only files with a com.agentdeck label matter
			// and those are well-formed. Log for the audit trail only.
			log.Debug("launchagent_unparsable", slog.String("path", path), slog.String("err", err.Error()))
			continue
		}
		agent.Path = path
		if !strings.HasPrefix(agent.Label, agentdeckLabelPrefix) {
			continue
		}
		if agent.Label == AutoupdateLabel {
			res.Skipped[agent.Label] = "update timer, never restarted from inside a run"
			log.Info("launchagent_skipped", slog.String("label", agent.Label), slog.String("reason", res.Skipped[agent.Label]))
			continue
		}
		prog := agent.ProgramPath()
		if !sameProgram(prog, exeRaw, exeReal) {
			res.Skipped[agent.Label] = fmt.Sprintf("program %s is not %s", prog, exeRaw)
			log.Info("launchagent_skipped", slog.String("label", agent.Label), slog.String("program", prog), slog.String("reason", "program is not the updated binary"))
			continue
		}

		if err := rebootstrapOne(opts, agent); err != nil {
			fmt.Fprintf(opts.Out, "  ✗ %s: %v\n", agent.Label, err)
			return res, err
		}
		res.Restarted = append(res.Restarted, agent.Label)
		fmt.Fprintf(opts.Out, "  ↻ %s re-registered with launchd\n", agent.Label)
	}
	return res, nil
}

// rebootstrapOne runs bootout, bootstrap and verify for a single agent.
func rebootstrapOne(opts RebootstrapOptions, agent LaunchAgent) error {
	log := opts.Logger
	domain := launchctlDomain(opts.UID)
	target := launchctlTarget(opts.UID, agent.Label)
	bootout := []string{"launchctl", "bootout", target}
	bootstrap := []string{"launchctl", "bootstrap", domain, agent.Path}
	printCmd := []string{"launchctl", "print", target}
	repair := []string{ShellQuote(bootout), ShellQuote(bootstrap)}
	fail := func(cause error) error {
		return &AgentRestartError{Label: agent.Label, Cause: cause, Repair: repair}
	}

	out, err := opts.Runner.Run(bootout...)
	switch {
	case err == nil:
		log.Info("launchagent_bootout", slog.String("label", agent.Label), slog.String("cmd", ShellQuote(bootout)))
	case launchctlNotLoaded(out, err):
		log.Info("launchagent_bootout_not_loaded", slog.String("label", agent.Label), slog.String("out", strings.TrimSpace(out)))
	default:
		log.Error("launchagent_bootout_failed", slog.String("label", agent.Label), slog.Int("exit", exitCode(err)), slog.String("out", strings.TrimSpace(out)))
		return fail(fmt.Errorf("`%s` failed (%v): %s", ShellQuote(bootout), err, firstLine(out)))
	}

	// bootout returns before the old instance is fully torn down; a bootstrap
	// issued in that window fails with "Input/output error" (5) or "service
	// already loaded". Retry briefly before giving up.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		out, err = opts.Runner.Run(bootstrap...)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("`%s` failed (%v): %s", ShellQuote(bootstrap), err, firstLine(out))
		log.Warn("launchagent_bootstrap_retry", slog.String("label", agent.Label), slog.Int("attempt", attempt+1), slog.String("out", strings.TrimSpace(out)))
		opts.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		log.Error("launchagent_bootstrap_failed", slog.String("label", agent.Label), slog.String("err", lastErr.Error()))
		return fail(lastErr)
	}
	log.Info("launchagent_bootstrap", slog.String("label", agent.Label), slog.String("cmd", ShellQuote(bootstrap)))

	out, err = opts.Runner.Run(printCmd...)
	if err != nil {
		log.Error("launchagent_verify_failed", slog.String("label", agent.Label), slog.String("out", strings.TrimSpace(out)))
		return fail(fmt.Errorf("`%s` failed after bootstrap (%v): %s", ShellQuote(printCmd), err, firstLine(out)))
	}
	if !(agent.KeepAlive || agent.RunAtLoad) {
		log.Info("launchagent_verified", slog.String("label", agent.Label), slog.String("state", "registered"))
		return nil
	}
	deadline := opts.now().Add(opts.VerifyTimeout)
	for {
		if launchctlState(out) == "running" {
			log.Info("launchagent_verified", slog.String("label", agent.Label), slog.String("state", "running"))
			return nil
		}
		if !opts.now().Before(deadline) {
			break
		}
		opts.Sleep(500 * time.Millisecond)
		out, err = opts.Runner.Run(printCmd...)
		if err != nil {
			return fail(fmt.Errorf("`%s` failed while waiting for it to start (%v): %s", ShellQuote(printCmd), err, firstLine(out)))
		}
	}
	state := launchctlState(out)
	log.Error("launchagent_not_running", slog.String("label", agent.Label), slog.String("state", state))
	return fail(fmt.Errorf("not running after %s (state = %s); check its log for EX_CONFIG", opts.VerifyTimeout, state))
}

// launchctlNotLoaded reports whether a bootout failure just means the service
// was not loaded (exit 3 "No such process", or the newer "Could not find
// service" wording), which is fine: the bootstrap that follows registers it.
func launchctlNotLoaded(out string, err error) bool {
	if exitCode(err) == 3 {
		return true
	}
	lower := strings.ToLower(out)
	return strings.Contains(lower, "no such process") || strings.Contains(lower, "could not find service")
}

// launchctlState extracts `state = ...` from `launchctl print` output.
func launchctlState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			return strings.TrimPrefix(line, "state = ")
		}
	}
	return "unknown"
}

// sameProgram reports whether prog names the updated executable, comparing
// the raw path and the symlink-resolved path on both sides.
func sameProgram(prog, exeRaw, exeReal string) bool {
	if prog == "" {
		return false
	}
	if prog == exeRaw || prog == exeReal {
		return true
	}
	progReal := resolveOrSelf(prog)
	return progReal == exeRaw || progReal == exeReal
}

func resolveOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// IsAgentRestartError reports whether err is an AgentRestartError.
func IsAgentRestartError(err error) bool {
	var e *AgentRestartError
	return errors.As(err, &e)
}
