package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// hooksStatusJSON is the one --json shape of every `<kind>-hooks status`
// (claude `hooks`, codex, gemini, hermes, cursor, pi, tmux). State is one of
// installed, not-installed, needs-reinstall, outdated, legacy, custom,
// foreign. Config is the file or slot that was inspected; Version is set only
// where the hook has one (pi); Details carries the extra text-mode lines.
type hooksStatusJSON struct {
	Kind    string   `json:"kind"`
	State   string   `json:"state"`
	Config  string   `json:"config,omitempty"`
	Version string   `json:"version,omitempty"`
	Details []string `json:"details,omitempty"`
}

// hooksStatusWantsJSON reports whether the status args ask for --json.
func hooksStatusWantsJSON(args []string) bool {
	for _, a := range args {
		switch a {
		case "--json", "-json", "--json=true", "-json=true":
			return true
		}
	}
	return false
}

func printHooksStatusJSON(st hooksStatusJSON) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing status: %v\n", err)
		exitCLI(1)
	}
}

func simpleHooksState(installed bool) string {
	if installed {
		return "installed"
	}
	return "not-installed"
}
