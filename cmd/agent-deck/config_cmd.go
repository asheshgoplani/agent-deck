package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleConfig(args []string) {
	if len(args) != 1 || args[0] != "orchestrate" {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck config orchestrate")
		os.Exit(1)
	}

	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	policy := cfg.ResolveOrchestrateToolPolicy()
	if err := json.NewEncoder(os.Stdout).Encode(policy); err != nil {
		fmt.Fprintf(os.Stderr, "Error: encode orchestrate policy: %v\n", err)
		os.Exit(1)
	}
}
