package main

import "testing"

func TestEnvironmentForTwoSequentialUpdatesReplacesSentinel(t *testing.T) {
	env := []string{"PATH=/bin", "AGENTDECK_UPDATED=v1.2.3"}
	env = environmentForUpdate(env, "v1.2.4")
	env = environmentForUpdate(env, "v1.2.5")
	var found []string
	for _, entry := range env {
		if len(entry) >= len(updatedEnvKey) && entry[:len(updatedEnvKey)] == updatedEnvKey {
			found = append(found, entry)
		}
	}
	if len(found) != 1 || found[0] != "AGENTDECK_UPDATED=v1.2.5" {
		t.Fatalf("sentinel entries = %v", found)
	}
}
