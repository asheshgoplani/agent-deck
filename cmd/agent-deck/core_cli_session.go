package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Registry-backed adapters for session start/stop/restart. Flags, help text,
// output and exit statuses are byte-identical to handleSessionStart,
// handleSessionStop and handleSessionRestart (asserted by
// TestCoreRegistryMatchesLegacyHandlers); the work itself runs in internal/core.

func cliSessionStart(profile string, args []string) {
	fs := flag.NewFlagSet("session start", flag.ExitOnError)
	var jsonOutput jsonModeFlag
	fs.Var(&jsonOutput, "json", "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	message := fs.String("message", "", "Initial message to send once agent is ready")
	messageShort := fs.String("m", "", "Initial message to send once agent is ready (short)")
	messageFile := fs.String("message-file", "", "Read the initial message from a file ('-' for stdin); avoids shell quoting of long prompts")
	yoloMode := fs.Bool("yolo", false, "Enable YOLO mode when starting Gemini or Codex sessions")
	attach := fs.Bool("attach", false, "Attach to the session after starting (requires an interactive terminal)")
	noWait := fs.Bool("no-wait", false, "Return as soon as the process is spawned instead of waiting up to 3s for the tool's session id (a caller that attaches right away; the id is still captured by hooks)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session start <id|title> [options]")
		fmt.Println()
		fmt.Println("Start a session's tmux process.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session start my-project")
		fmt.Println("  agent-deck session start my-project --message \"Research MCP patterns\"")
		fmt.Println("  agent-deck session start my-project -m \"Explain this codebase\"")
		fmt.Println("  agent-deck session start my-project --message-file task.md   # long prompt from file, no shell quoting")
		fmt.Println("  git diff | agent-deck session start my-project --message-file -   # initial message from stdin")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(jsonOutput.enabled(), *quiet || *quietShort)

	initialMessage, err := resolveMessageInput(mergeFlags(*message, *messageShort), *messageFile, os.Stdin)
	if err != nil {
		exitCLIError(out, &jsonOutput, core.IDSessionStart, core.CodeInvalidInput, err.Error(), 1)
	}

	res := runCore(core.IDSessionStart, core.SessionStartIn{
		Profile: profile,
		Session: fs.Arg(0),
		Message: initialMessage,
		Yolo:    *yoloMode,
		NoWait:  *noWait,
	}, nil)
	if res.Err != nil {
		exitCoreError(out, &jsonOutput, res, nil)
	}
	started := res.Out.(core.SessionStartOut)

	if started.Status == core.StartStatusQueued {
		if jsonOutput.envelope() {
			printEnvelope(res)
			return
		}
		out.Success(
			fmt.Sprintf("Queued session: %s (group at cap %d)", started.Title, started.MaxConcurrent),
			map[string]interface{}{
				"success":        true,
				"id":             started.ID,
				"title":          started.Title,
				"status":         "queued",
				"group":          started.Group,
				"max_concurrent": started.MaxConcurrent,
			},
		)
		return
	}

	// --attach suspends the CLI into tmux until the user detaches, so the
	// success output is skipped. Refused loudly without a terminal or under
	// --json; the session stays started in both cases.
	if *attach {
		if jsonOutput.enabled() {
			exitCLIError(out, &jsonOutput, core.IDSessionStart, core.CodeInvalidInput, "--attach cannot be combined with --json; session was started", 3)
		}
		if err := attachInstanceInteractive(started.Instance); err != nil {
			if errors.Is(err, errAttachNoTTY) {
				fmt.Fprintf(os.Stderr, "Error: %v; session was started\n", err)
				os.Exit(3)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to attach: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if jsonOutput.envelope() {
		printEnvelope(res)
		return
	}
	jsonData := map[string]interface{}{
		"success": true,
		"id":      started.ID,
		"title":   started.Title,
	}
	if started.Tmux != "" {
		jsonData["tmux"] = started.Tmux
	}
	if started.ClaudeSessionID != "" {
		jsonData["claude_session_id"] = started.ClaudeSessionID
	}
	if started.Message != "" {
		jsonData["message"] = started.Message
		jsonData["message_pending"] = false
		out.Success(fmt.Sprintf("Started session: %s (message sent)", started.Title), jsonData)
	} else {
		out.Success(fmt.Sprintf("Started session: %s", started.Title), jsonData)
	}
}

func cliSessionStop(profile string, args []string) {
	fs := flag.NewFlagSet("session stop", flag.ExitOnError)
	var jsonOutput jsonModeFlag
	fs.Var(&jsonOutput, "json", "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session stop <id|title> [options]")
		fmt.Println()
		fmt.Println("Stop/kill a session's process (tmux session remains).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(jsonOutput.enabled(), *quiet || *quietShort)

	res := runCore(core.IDSessionStop, core.SessionStopIn{Profile: profile, Session: fs.Arg(0)}, func(ev core.Event) {
		if ev.Kind == core.EventQueueDrainFailed {
			fmt.Fprintf(os.Stderr, "queue drain failed to start %s: %v\n", ev.Title, ev.Err)
		}
	})
	if res.Err != nil {
		exitCoreError(out, &jsonOutput, res, nil)
	}
	stopped := res.Out.(core.SessionStopOut)

	if jsonOutput.envelope() {
		printEnvelope(res)
	} else {
		result := map[string]interface{}{
			"success": true,
			"id":      stopped.ID,
			"title":   stopped.Title,
		}
		if stopped.Drained != "" {
			result["drained"] = stopped.Drained
			result["drained_title"] = stopped.DrainedTitle
		}
		out.Success(fmt.Sprintf("Stopped session: %s", stopped.Title), result)
	}
	// Journal only after the verdict is printed.
	res.Finish()
}

func cliSessionRestart(profile string, args []string) {
	fs := flag.NewFlagSet("session restart", flag.ExitOnError)
	var jsonOutput jsonModeFlag
	fs.Var(&jsonOutput, "json", "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Restart even if the session is already healthy and fresh (bypasses issue #30 guard)")
	all := fs.Bool("all", false, "Restart all active sessions")
	envFlags := make(envVarFlags)
	fs.Var(&envFlags, "env", "Environment variable in KEY=VALUE format for the restarted process (can be repeated)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session restart [id|title] [options]")
		fmt.Println()
		fmt.Println("Restart a session. For Claude sessions, this reloads MCPs.")
		fmt.Println()
		fmt.Println("By default, a restart is skipped (no-op) when the session is already")
		fmt.Println("healthy (running/waiting/idle/starting) and was started within the last")
		fmt.Println("60 seconds. This prevents watchdog double-fires from destroying a")
		fmt.Println("just-created tmux scope (issue #30). Use --force to restart anyway.")
		fmt.Println()
		fmt.Println("A restart is also skipped when the session's agent could not authenticate")
		fmt.Println("(401 / invalid credentials): a restart cannot fix a credential, and each")
		fmt.Println("attempt races the rotating token shared by every session on this host.")
		fmt.Println("Re-authenticate (run /login), then restart — --force overrides the hold.")
		fmt.Println()
		fmt.Println("--all paces restarts with a jittered stagger, caps how many un-verified")
		fmt.Println("boots run at once, skips auth-held sessions, and STOPS early if several")
		fmt.Println("restarts in a row die on authentication (reported as auth_tripped).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session restart my-project")
		fmt.Println("  agent-deck session restart my-project --env API_URL=https://api.example.com")
		fmt.Println("  agent-deck session restart my-project --env FOO=one --env BAR=two")
		fmt.Println("  agent-deck session restart --all")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(jsonOutput.enabled(), quietMode)
	human := !jsonOutput.enabled()

	res := runCore(core.IDSessionRestart, core.SessionRestartIn{
		Profile: profile,
		Session: fs.Arg(0),
		All:     *all,
		Force:   *force,
		Env:     envFlags,
	}, func(ev core.Event) {
		if !human {
			return
		}
		switch {
		case ev.Kind == core.EventRestartWarning && !*all:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", ev.Message)
		case ev.Kind == core.EventRestartWarning:
			fmt.Fprintf(os.Stderr, "  Warning: %s\n", ev.Message)
		case ev.Kind == core.EventRestartBegin:
			fmt.Printf("Restarting %s...\n", ev.Title)
		case ev.Kind == core.EventRestartFailed:
			fmt.Fprintf(os.Stderr, "  Error: %s\n", ev.Message)
		case ev.Kind == core.EventRestartDone:
			fmt.Printf("  Done: %s\n", ev.Title)
		case ev.Kind == core.EventRestartSkipped && !quietMode:
			fmt.Printf("Skipped %s: %s\n", ev.Title, ev.Message)
		}
	})
	if res.Err != nil {
		exitCoreError(out, &jsonOutput, res, fs.Usage)
	}
	restarted := res.Out.(core.SessionRestartOut)

	if restarted.All != nil {
		renderRestartAll(out, &jsonOutput, res, restarted.All)
		return
	}

	switch {
	case jsonOutput.envelope():
		printEnvelope(res)
	case restarted.Skipped:
		out.Success(fmt.Sprintf("Skipped restart of %s: %s", restarted.Title, restarted.Reason), map[string]interface{}{
			"success": true,
			"skipped": true,
			"reason":  restarted.Reason,
			"id":      restarted.ID,
			"title":   restarted.Title,
		})
	default:
		data := map[string]interface{}{
			"success": true,
			"id":      restarted.ID,
			"title":   restarted.Title,
		}
		if restarted.Warning != "" {
			data["warning"] = restarted.Warning
		}
		out.Success(fmt.Sprintf("Restarted session: %s", restarted.Title), data)
	}
	res.Finish()
}

// renderRestartAll prints the restart --all summary, journals the restarts
// and exits non-zero on a failed or tripped sweep.
func renderRestartAll(out *CLIOutput, mode *jsonModeFlag, res *core.Result, all *core.RestartAllOut) {
	if all.TripMessage != "" && !out.jsonMode {
		fmt.Fprintf(os.Stderr, "\n🔒 %s\n", all.TripMessage)
	}

	switch {
	case mode.envelope():
		printEnvelope(res)
	case out.jsonMode:
		sweep := session.BootSweepResult{
			Booted:      all.Restarted,
			Failed:      all.Failed,
			SkippedHeld: all.SkippedAuth,
			AuthDeaths:  all.AuthDeaths,
			Abandoned:   all.Abandoned,
			Tripped:     all.AuthTripped,
			TripMessage: all.TripMessage,
		}
		out.Success("", restartAllSessionsJSONPayload(all.Total, sweep, restartRowsJSON(all.Sessions)))
	case !out.quietMode:
		fmt.Printf("Restarted %d/%d sessions", all.Restarted, all.Total)
		if all.Failed > 0 {
			fmt.Printf(" (%d failed)", all.Failed)
		}
		if all.SkippedAuth > 0 {
			fmt.Printf(" (%d held for auth)", all.SkippedAuth)
		}
		if all.Abandoned > 0 {
			fmt.Printf(" (%d abandoned after auth circuit tripped)", all.Abandoned)
		}
		fmt.Println()
	}

	res.Finish()
	if !all.OK() {
		os.Exit(1)
	}
}

// restartRowsJSON renders restart --all rows in the legacy per-session shape.
func restartRowsJSON(rows []core.RestartRow) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		m := map[string]interface{}{"id": row.ID, "title": row.Title}
		if row.Success != nil {
			m["success"] = *row.Success
		}
		if row.Error != "" {
			m["error"] = row.Error
		}
		if row.SpawnFailure != nil {
			m["spawn_failure"] = spawnFailureJSON(row.SpawnFailure)
		}
		if row.Warning != "" {
			m["warning"] = row.Warning
		}
		if row.Skipped {
			m["skipped"] = true
			m["reason"] = row.Reason
		}
		if row.AuthDeath {
			m["auth_death"] = true
		}
		out = append(out, m)
	}
	return out
}
