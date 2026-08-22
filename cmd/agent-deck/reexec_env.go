package main

import "strings"

const updatedEnvKey = "AGENTDECK_UPDATED="

func environmentForUpdate(environ []string, version string) []string {
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, updatedEnvKey) {
			result = append(result, entry)
		}
	}
	return append(result, updatedEnvKey+version)
}
