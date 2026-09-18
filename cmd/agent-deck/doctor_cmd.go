package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output account and runtime health diagnostics as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck doctor [--json]\n\nReport local runtime health and check named Claude slots configured as [profiles.<name>.claude].config_dir.\nWarn when slots share a directory; missing or unreadable paths remain unknown.\nAccount checks read directory metadata; health reads local samples. Neither verifies live login identities.\nAlso lists untracked tmux sessions (agentdeck_ prefix, not in `list --json`) so you can decide whether to keep or stop them; never stops any itself.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor does not accept positional arguments")
		os.Exit(2)
	}
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config: %q\n", err.Error())
		os.Exit(1)
	}
	runtimeHealth, err := readRuntimeHealth("", time.Hour)
	if err != nil {
		// Report returns its schema and budgets even when history cannot be read.
		runtimeHealth.Flags = append(runtimeHealth.Flags, "runtime health unknown: "+strconv.QuoteToASCII(err.Error()))
	}
	runtimeHealth.UntrackedTmuxSessions = untrackedTmuxSessionsForHealth("")
	slots := session.DiagnoseClaudeAccountDirectories(config)
	if *jsonOutput {
		report := struct {
			AccountSlots []session.AccountDirectoryDiagnostic `json:"account_slots"`
			Health       health.Summary                       `json:"health"`
		}{slots, runtimeHealth}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode diagnostics: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(health.Format(runtimeHealth))
	fmt.Println("Named Claude account directories:")
	if len(slots) == 0 {
		fmt.Println("No named Claude account slots configured.")
		return
	}
	for _, slot := range slots {
		fmt.Printf("%s slot %q: %q (path: %s)", strings.ToUpper(slot.State), slot.Name, slot.ConfigDir, slot.PathState)
		if len(slot.SharedWith) > 0 {
			fmt.Print("; shared with")
			for _, name := range slot.SharedWith {
				fmt.Printf(" %q", name)
			}
		}
		if slot.Reason != "" {
			fmt.Printf("; %s", slot.Reason)
		}
		fmt.Println()
	}
}
