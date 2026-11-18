package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/termtext"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// voiceSub is one `agent-deck voice <name>` subcommand. To add one (for
// example `do`), append an entry to voiceSubs from the new file's init():
// the dispatcher, usage text and the unknown-name exit 2 need no change:
//
//	func init() { voiceSubs = append(voiceSubs, voiceSub{name: "do", ...}) }
type voiceSub struct {
	name    string
	summary string // one line in the usage text
	usage   string // optional extra usage lines, printed under the summary
	// run gets the args after the name and returns the exit code.
	// 0 = ok, 2 = usage error, 1 = internal error.
	run func(profile string, args []string, stdout, stderr io.Writer) int
}

var voiceSubs []voiceSub

func init() {
	voiceSubs = append(voiceSubs, voiceSub{
		name:    "state",
		summary: "Voice state: on/off, phase, mic target [--json]",
		usage:   "  agent-deck voice state --json   {on, phase (listening|hearing|thinking|speaking|resting|off), extra (muted|call|meeting or empty), phaseSince, muted, target, source, from, updatedAt}",
		run:     runVoiceState,
	})
}

func writeVoiceUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck [-p profile] voice <subcommand> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	for _, s := range voiceSubs {
		fmt.Fprintf(w, "  %-8s %s\n", s.name, s.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Details:")
	for _, s := range voiceSubs {
		if s.usage != "" {
			fmt.Fprintln(w, s.usage)
		}
	}
}

// runVoice dispatches `voice <sub>`. Unknown or missing sub: usage on stderr, 2.
func runVoice(profile string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeVoiceUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		writeVoiceUsage(stdout)
		return 0
	}
	for _, s := range voiceSubs {
		if s.name == args[0] {
			return s.run(profile, args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "Unknown voice subcommand %q\n\n", args[0])
	writeVoiceUsage(stderr)
	return 2
}

func handleVoice(profile string, args []string) {
	if code := runVoice(profile, args, os.Stdout, os.Stderr); code != 0 {
		exitCLI(code)
	}
}

func runVoiceState(_ string, args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		case "--help", "-h":
			fmt.Fprintln(stdout, "Usage: agent-deck voice state [--json]")
			return 0
		default:
			fmt.Fprintf(stderr, "voice state: unknown argument %q\n\n", a)
			writeVoiceUsage(stderr)
			return 2
		}
	}
	now := time.Now()
	st := voicestate.NewReader(voicestate.DefaultDir()).Poll()
	if asJSON {
		b, err := json.Marshal(voicestate.NewView(st, now))
		if err != nil {
			fmt.Fprintln(stderr, "voice state:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprintln(stdout, voiceStateLine(voicestate.NewView(st, now)))
	return 0
}

// voiceStateLine is the plain-text form; free text from the state file is
// cleaned of control characters first.
func voiceStateLine(v voicestate.View) string {
	if !v.On {
		return "voice: off"
	}
	line := "voice: " + v.Phase
	switch {
	case v.Extra != "" && v.Muted && v.Extra != "muted":
		// A rest for a call or a meeting while also muted says both.
		line += " (" + v.Extra + ", muted)"
	case v.Extra != "":
		line += " (" + v.Extra + ")"
	case v.Muted:
		line += " (muted)"
	}
	if v.Target != nil {
		line += " on " + termtext.Clean(v.Target.Title)
		if v.Target.Host != "" {
			line += " [" + termtext.Clean(v.Target.Host) + "]"
		}
	}
	return strings.TrimSpace(line)
}
