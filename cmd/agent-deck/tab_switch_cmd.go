package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// handleTabSwitch maps a tab number (1-9) to an instance and writes
// a switch request file, then detaches the tmux client. All errors
// are swallowed so this never disrupts an active terminal session.
func handleTabSwitch(args []string) {
	if len(args) < 1 {
		return
	}

	tabNum, err := strconv.Atoi(args[0])
	if err != nil || tabNum < 1 || tabNum > 9 {
		return
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	dbPath := filepath.Join(homeDir, ".agent-deck", "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	instances, err := db.LoadInstances()
	if err != nil {
		return
	}

	idx := tabNum - 1
	if idx >= len(instances) {
		return
	}

	targetID := instances[idx].ID
	adDir := filepath.Join(homeDir, ".agent-deck")

	_ = os.WriteFile(filepath.Join(adDir, "tab_switch_request"), []byte(targetID), 0644)
	_ = os.WriteFile(filepath.Join(adDir, "tab_current"), []byte(targetID), 0644)

	_ = exec.Command("tmux", "detach-client").Run()
}
