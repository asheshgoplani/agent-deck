package main

import (
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// applyClaudeCwdSync propagates Claude Code's `/cd` working-directory change
// (v2.1.169+) into the persisted project_path for instanceID. Silent no-op on
// any failure — the hook must not fail the Claude turn.
func applyClaudeCwdSync(instanceID, newCwd string) {
	if instanceID == "" || newCwd == "" {
		return
	}

	profiles, err := session.ListProfiles()
	if err != nil || len(profiles) == 0 {
		p := os.Getenv("AGENTDECK_PROFILE")
		if p == "" {
			p = session.DefaultProfile
		}
		profiles = []string{p}
	}

	for _, profile := range profiles {
		storage, err := session.NewStorageWithProfile(profile)
		if err != nil {
			continue
		}
		found, _ := storage.SyncInstanceCwd(instanceID, newCwd)
		_ = storage.Close()
		if found {
			return
		}
	}
}
