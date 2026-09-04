package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck session primer [id] [--json]` — the self-service fact sheet for
// v1.16.0 session context injection. Any agent (on any harness — this is the
// universal fallback the env spine points at) can run it to learn who it is,
// where it runs, who it reports to, and which cheap CLI paths exist. With no
// id it resolves the CALLER's own session from AGENTDECK_INSTANCE_ID /
// AGENTDECK_SESSION_ID / the tmux session, exactly like `session show`.
//
// NOT named `session context` — that command already exists and means
// "inspect this session's context-window usage".
func handleSessionPrimer(profile string, args []string) {
	fs := flag.NewFlagSet("session primer", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output the fact sheet as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session primer [id|title] [options]")
		fmt.Println()
		fmt.Println("Print the session context primer: identity, location, lifecycle, and")
		fmt.Println("the cheap agent-deck command paths. Auto-detects the calling session")
		fmt.Println("when no id is given. Facts that cannot be determined print \"unknown\".")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
	}

	cfg, _ := session.LoadUserConfig() // nil degrades to default resolution

	// Lifecycle for a CLI query: the same tool-aware signal the spawn
	// builders use. For a session that is already running this reports what
	// the current/next spawn means (a bound conversation id ⇒ resumed).
	lifecycle := inst.LifecycleAtLaunch()

	parentTitle := ""
	if inst.ParentSessionID != "" {
		for _, candidate := range instances {
			if candidate.ID == inst.ParentSessionID {
				parentTitle = candidate.GetTitleThreadSafe()
				break
			}
		}
	}

	facts := session.CollectPrimerFacts(cfg, inst, parentTitle, lifecycle)

	// Human/agent-readable form: render at the effective level, but never
	// below primer — asking for the fact sheet IS the opt-in, so a level
	// of "none" (which gates automatic injection) must not blank the
	// explicit query.
	level := facts.Level
	if level == session.ContextLevelNone {
		level = session.ContextLevelPrimer
	}
	// Route through the shared CLIOutput helper so --json/-q behave like
	// every other session command (round-1 P2 parity: a bare fmt.Println
	// ignored quiet mode).
	out.Print(session.RenderPrimer(facts, level)+"\n", facts)
}
