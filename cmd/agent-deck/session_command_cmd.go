package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/slashcmd"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// defaultBackgroundWaitIdle bounds how long a --background run waits for the
// target (usually the calling session itself) to finish its turn.
const defaultBackgroundWaitIdle = 5 * time.Minute

// splitCommandArgs maps the positional args of `session command` to a session
// reference and the slash command. A lone "/..." argument, or the reference
// "current", targets the session owning the current tmux pane ("").
func splitCommandArgs(args []string) (ref, command string, err error) {
	switch len(args) {
	case 1:
		if !strings.HasPrefix(strings.TrimSpace(args[0]), "/") {
			return "", "", fmt.Errorf("missing slash command (usage: session command [id] \"/cmd args\")")
		}
		return "", args[0], nil
	case 2:
		ref = args[0]
		if ref == "current" {
			ref = ""
		}
		return ref, args[1], nil
	default:
		return "", "", fmt.Errorf("expected [id] \"/cmd args\" (quote the slash command as one argument)")
	}
}

// backgroundArgs rebuilds the CLI args for the detached child: same command,
// no --background, JSON output, and a bounded --wait-idle.
func backgroundArgs(profile, ref, command string, timeout, waitIdle time.Duration) []string {
	if waitIdle <= 0 {
		waitIdle = defaultBackgroundWaitIdle
	}
	if ref == "" {
		ref = "current"
	}
	return profileArgs(profile, "session", "command", "--json", "--timeout", timeout.String(), "--wait-idle", waitIdle.String(), ref, command)
}

// resolveCommandTarget loads the profile holding the target session. For the
// current session the profile is detected the way `session current` does it.
func resolveCommandTarget(profile, ref string) (*session.Storage, []*session.Instance, []*session.GroupData, *session.Instance, string, string, string) {
	if ref == "" {
		if os.Getenv("TMUX") == "" {
			return nil, nil, nil, nil, "", "no session given and not inside tmux", ErrCodeNotFound
		}
		name, err := getCurrentTmuxSessionName()
		if err != nil {
			return nil, nil, nil, nil, "", "cannot read the current tmux session: " + err.Error(), ErrCodeNotFound
		}
		preferred := profile
		if preferred == "" || preferred == session.DefaultProfile {
			if preferred, err = session.ResolveProfileForStorage(""); err != nil {
				return nil, nil, nil, nil, "", "failed to resolve profile: " + err.Error(), ErrCodeNotFound
			}
		}
		data, found := findInstanceDataByTmuxFast(name, preferred)
		if data == nil {
			return nil, nil, nil, nil, "", "the current tmux session is not an agent-deck session", ErrCodeNotFound
		}
		if found != "" {
			profile = found
		}
		ref = data.ID
	}
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		return nil, nil, nil, nil, "", err.Error(), ErrCodeNotFound
	}
	inst, msg, code := ResolveSession(ref, instances)
	return storage, instances, groups, inst, profile, msg, code
}

func handleSessionCommand(profile string, args []string) {
	fs := flag.NewFlagSet("session command", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output the result as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	dryRun := fs.Bool("dry-run", false, "Read the screen and print the plan; type nothing")
	timeout := fs.Duration("timeout", 120*time.Second, "How long to wait for the outcome after submitting (compaction can take minutes)")
	waitIdle := fs.Duration("wait-idle", 0, "Wait up to this long for the session to be idle with an empty composer (0 = check once)")
	background := fs.Bool("background", false, "Detach and run once the session is idle (for a session targeting itself); prints the result file")
	resultFile := fs.String("result-file", "", "With --background: where the detached run writes its JSON result")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session command [id|current] \"/command [args]\" [options]")
		fmt.Println()
		fmt.Println("Run a Claude Code slash command (/model, /compact, /clear, /exit, any skill)")
		fmt.Println("in a session and verify the outcome from the screen. Refuses, typing nothing,")
		fmt.Println("when the composer holds unsent text, a turn is running, or a dialog is open.")
		fmt.Println("Without an id the session owning the current tmux pane is used.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session command api \"/model claude-haiku-5-5\" --json")
		fmt.Println("  agent-deck session command api /compact --timeout 5m")
		fmt.Println("  agent-deck session command --background \"/clear\"   # from inside the session itself")
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, *quiet)

	ref, text, err := splitCommandArgs(fs.Args())
	if err != nil {
		fs.Usage()
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	cmd, err := slashcmd.Parse(text)
	if err != nil {
		code := slashcmd.CodeInvalid
		if errors.Is(err, slashcmd.ErrRefused) {
			code = slashcmd.CodeRefused
		}
		emitCommandResult(out, slashcmd.Result{Command: strings.TrimSpace(text), Code: code, Error: err.Error(), Evidence: []string{}, Steps: []string{}})
		exitCLI(1)
	}

	storage, instances, groups, inst, resolvedProfile, msg, code := resolveCommandTarget(profile, ref)
	if inst == nil {
		if *jsonOutput {
			// The same result contract as every other failure, exit 2.
			emitCommandResult(out, slashcmd.Result{Command: cmd.Text, Kind: cmd.Kind, Code: "not_found", Error: msg, Evidence: []string{}, Steps: []string{}})
		} else {
			out.Error(msg, code)
		}
		exitCLI(2)
		return
	}
	res := slashcmd.Result{Session: inst.Title, ID: inst.ID, Command: cmd.Text, Kind: cmd.Kind, Evidence: []string{}, Steps: []string{}}
	if !session.IsClaudeCompatible(inst.Tool) {
		res.Code, res.Error = "unsupported_tool", fmt.Sprintf("session '%s' runs %s; slash commands are supported for claude sessions only", inst.Title, inst.Tool)
		emitCommandResult(out, res)
		exitCLI(1)
	}
	if !inst.Exists() {
		res.Code, res.Error = "not_running", fmt.Sprintf("session '%s' is not running", inst.Title)
		emitCommandResult(out, res)
		exitCLI(1)
	}

	if *background && !*dryRun {
		path, err := startBackgroundCommand(resolvedProfile, inst.ID, cmd.Text, *timeout, *waitIdle, *resultFile)
		if err != nil {
			res.Code, res.Error = slashcmd.CodeKeys, "could not start the background run: "+err.Error()
			emitCommandResult(out, res)
			exitCLI(1)
		}
		res.OK, res.Queued, res.Result = true, true, path
		res.Steps = append(res.Steps, "queued: runs when the session is idle with an empty composer")
		emitCommandResult(out, res)
		return
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		res.Code, res.Error = "not_running", fmt.Sprintf("session '%s' has no tmux pane", inst.Title)
		emitCommandResult(out, res)
		exitCLI(1)
	}

	if tmuxSess.VimMode {
		// Typing into a vim-mode composer in normal mode runs motions, and
		// Escape leaves insert mode, so neither typing nor the abort path
		// (Escape, Ctrl+U) is safe there.
		res.Code, res.Error = "unsupported_vim_mode", fmt.Sprintf("session '%s' uses the Claude vim composer; slash commands are not supported there yet", inst.Title)
		emitCommandResult(out, res)
		exitCLI(1)
	}

	// The session's Claude config dir holds the default model for new
	// sessions (settings.json "model"); /model here must never change it.
	// "before" is read right before the Enter that submits /model, after
	// any --wait-idle, so a change the user made elsewhere while we waited
	// is not ours.
	opts := slashcmd.Options{Timeout: *timeout, WaitIdle: *waitIdle, DryRun: *dryRun}
	var configDir string
	var defaultBefore defaultModel
	if cmd.Kind == slashcmd.KindModel && !*dryRun {
		configDir = commandConfigDir(inst, tmuxSess)
		opts.BeforeSubmit = func() { defaultBefore = readDefaultModel(configDir) }
	}
	run := slashcmd.Run(tmuxSess, cmd, opts)
	run.Session, run.ID = inst.Title, inst.ID
	if configDir != "" && hasStep(run, "submitted") {
		guardDefaultModel(&run, configDir, defaultBefore, cmd.Args)
	}
	if run.OK && cmd.Kind == slashcmd.KindModel && !*dryRun {
		persistSwitchedModel(resolvedProfile, inst.ID, cmd.Args, &run)
	}

	if run.OK && cmd.Kind == slashcmd.KindExit && !*dryRun {
		// Claude exited; finish with the same stop path as `session stop`
		// so agent-deck records the session as stopped. Only this instance.
		if _, err := stopInstanceAndSave(storage, inst, instances, groups); err != nil {
			run.OK, run.Code, run.Error = false, "stop_failed", "Claude exited but agent-deck could not record the stop: "+err.Error()
		} else {
			run.After = "stopped"
			run.Evidence = append(run.Evidence, "agent-deck status: "+StatusString(inst.Status))
			run.Steps = append(run.Steps, "session stopped (session stop path)")
			session.RecordSessionEvent(resolvedProfile, inst.ID, health.KindStop, nil)
			session.RecallNotifyInstance(inst, health.KindStop)
		}
	}
	emitCommandResult(out, run)
	if !run.OK {
		exitCLI(1)
	}
}

// emitCommandResult prints the result: the full JSON contract with --json,
// one short confirmation line otherwise.
func emitCommandResult(out *CLIOutput, res slashcmd.Result) {
	if out.jsonMode {
		out.printJSON(res)
		return
	}
	if !res.OK {
		fmt.Fprintf(os.Stderr, "Error: %s did not run: %s (%s)\n", res.Command, res.Error, res.Code)
		return
	}
	if out.quietMode {
		return
	}
	switch {
	case res.Queued:
		fmt.Printf("Queued %s for %s; it runs when the session is idle. Result: %s\n", res.Command, res.Session, res.Result)
	case len(res.Plan) > 0:
		fmt.Printf("Dry run for %s on %s:\n  %s\n", res.Command, res.Session, strings.Join(res.Plan, "\n  "))
	default:
		line := fmt.Sprintf("%s on %s: %s", res.Command, res.Session, res.After)
		if res.Before != "" {
			line = fmt.Sprintf("%s on %s: %s -> %s", res.Command, res.Session, res.Before, res.After)
		}
		fmt.Println(line)
	}
}

// startBackgroundCommand re-executes this binary detached from the caller's
// process group so it survives the calling Claude tool call, and returns the
// result file path.
func startBackgroundCommand(profile, id, command string, timeout, waitIdle time.Duration, resultFile string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resultFile == "" {
		f, err := os.CreateTemp("", "agent-deck-command-*.json")
		if err != nil {
			return "", err
		}
		resultFile = f.Name()
		_ = f.Close()
	}
	resultFile, _ = filepath.Abs(resultFile)
	f, err := os.OpenFile(resultFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	// self is this binary (os.Executable) and argv goes straight to execve
	// with no shell; id was resolved from storage, command passed Parse.
	child := exec.Command(self, backgroundArgs(profile, id, command, timeout, waitIdle)...) //nolint:gosec // G702: own binary, no shell
	child.Stdin, child.Stdout, child.Stderr = nil, f, f
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		_ = f.Close() // nothing was written; the start error is the one to report
		return "", err
	}
	// The child holds its own copy of the descriptor; this process wrote
	// nothing, but a failed close is still reported rather than dropped.
	if err := f.Close(); err != nil {
		_ = child.Process.Release()
		return "", fmt.Errorf("close result file: %w", err)
	}
	return resultFile, child.Process.Release()
}

func handleSessionCommands(profile string, args []string) {
	fs := flag.NewFlagSet("session commands", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session commands [id|current] [--json]")
		fmt.Println()
		fmt.Println("List the slash commands and skills a Claude session can run with")
		fmt.Println("`session command`: built-ins, user and project commands and skills, and")
		fmt.Println("enabled plugins. Reads files only; nothing is typed into the session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	ref := fs.Arg(0)
	if ref == "current" {
		ref = ""
	}
	_, _, _, inst, _, msg, code := resolveCommandTarget(profile, ref)
	if inst == nil {
		out.Error(msg, code)
		exitCLI(2)
		return
	}
	if !session.IsClaudeCompatible(inst.Tool) {
		out.Error(fmt.Sprintf("session '%s' runs %s; slash commands are supported for claude sessions only", inst.Title, inst.Tool), "unsupported_tool")
		exitCLI(1)
	}
	entries := slashcmd.Discover(session.ClaudeConfigDirForSend(inst), inst.ProjectPath)
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%-28s %-20s %s\n", e.Name, e.Source, truncateDescription(e.Description, 90))
	}
	out.Print(b.String(), map[string]interface{}{"session": inst.Title, "session_id": inst.ID, "commands": entries})
}

func truncateDescription(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// defaultModel is the "model" key of a Claude config dir's settings.json,
// the default for new sessions that typed "/model <id>" overwrites.
type defaultModel struct {
	value string
	found bool
}

// settingsReadFile reads settings.json; tests replace it.
var settingsReadFile = os.ReadFile

func readDefaultModel(configDir string) defaultModel {
	for attempt := 0; ; attempt++ {
		var settings struct {
			Model *string `json:"model"`
		}
		data, err := settingsReadFile(filepath.Join(configDir, "settings.json"))
		if err != nil {
			return defaultModel{}
		}
		if json.Unmarshal(data, &settings) != nil {
			if attempt == 0 {
				// Claude may be rewriting it: read once more.
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return defaultModel{}
		}
		if settings.Model == nil {
			return defaultModel{}
		}
		return defaultModel{value: *settings.Model, found: true}
	}
}

// guardDefaultModel fails res with default_changed when the saved default
// for new sessions changed while /model ran AND the new value is the model
// that was requested (the switch saved it). A change to some other value
// was made elsewhere (another session sharing the config dir): it is noted
// as evidence and does not fail the switch. Nothing is ever restored or
// suggested for restoring: the value may be the user's deliberate choice.
func guardDefaultModel(res *slashcmd.Result, configDir string, before defaultModel, requested string) {
	after := readDefaultModel(configDir)
	path := filepath.Join(configDir, "settings.json")
	if after == before {
		if before.found {
			res.Evidence = append(res.Evidence, fmt.Sprintf(`settings.json "model" unchanged (%q)`, before.value))
		}
		return
	}
	change := fmt.Sprintf(`settings.json "model": %q -> %q (%s)`, before.value, after.value, path)
	if !after.found || !(slashcmd.ModelMatches(requested, after.value) || (res.After != "" && slashcmd.ModelMatches(res.After, after.value))) {
		res.Evidence = append(res.Evidence, change+"; not the requested model, so changed elsewhere")
		return
	}
	res.OK, res.Code = false, "default_changed"
	res.Error = fmt.Sprintf("the switch also saved %s as the default model for new sessions (%s)", after.value, change)
	res.Evidence = append(res.Evidence, change)
}

func hasStep(res slashcmd.Result, step string) bool {
	return slices.Contains(res.Steps, step)
}

// commandConfigDir is the Claude config dir the session runs with: the
// CLAUDE_CONFIG_DIR in its tmux environment when set (a custom wrapper or a
// launch-time override), else the one agent-deck resolves for it.
func commandConfigDir(inst *session.Instance, tmuxSess *tmux.Session) string {
	if tmuxSess != nil {
		if v, err := tmuxSess.GetEnvironment("CLAUDE_CONFIG_DIR"); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return session.ClaudeConfigDirForSend(inst)
}

// persistSwitchedModel stores a verified /model switch on the instance, so
// a restart, a crash restart or a fork starts on that model (--model)
// instead of reverting to the launch or default model. The typed id or
// alias is stored: Claude's picker shows display names, not ids. "default"
// clears the stored model (restart then uses the configured default). A
// display label ("Haiku 5.5") is not a --model value and is not stored.
//
// The registry is re-read here (the switch can take minutes) so only this
// session's model changes.
func persistSwitchedModel(profile, id, arg string, res *slashcmd.Result) {
	model := strings.TrimSpace(arg)
	if slashcmd.IsDefaultModel(model) {
		model = "" // clear: back to the configured default on restart
	} else if model == "" || strings.ContainsAny(model, " \t") {
		res.Evidence = append(res.Evidence, "model not stored for restart: "+model+" is not a --model value")
		return
	}
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		res.Evidence = append(res.Evidence, "model not stored for restart: "+err.Error())
		return
	}
	defer storage.Close()
	var inst *session.Instance
	for _, candidate := range instances {
		if candidate.ID == id {
			inst = candidate
		}
	}
	if inst == nil {
		res.Evidence = append(res.Evidence, "model not stored for restart: session no longer in the registry")
		return
	}
	_, postCommit, err := session.SetField(inst, session.FieldModel, model, nil)
	if err != nil {
		res.Evidence = append(res.Evidence, "model not stored for restart: "+err.Error())
		return
	}
	if postCommit != nil {
		postCommit()
	}
	if err := saveSessionData(storage, instances, groups); err != nil {
		res.Evidence = append(res.Evidence, "model not stored for restart: "+err.Error())
		return
	}
	if model == "" {
		res.Steps = append(res.Steps, "cleared the session's stored model: restart and fork use the configured default")
		return
	}
	res.Steps = append(res.Steps, "stored "+model+" as the session's model for restart and fork")
}
