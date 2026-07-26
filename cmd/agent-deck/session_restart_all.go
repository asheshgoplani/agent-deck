package main

import "github.com/asheshgoplani/agent-deck/internal/session"

func restartAllSessionRecords(results map[string]map[string]interface{}, attempts []session.BootAttempt) []map[string]interface{} {
	ordered := make([]map[string]interface{}, 0, len(attempts))
	for _, attempt := range attempts {
		result := results[attempt.InstanceID]
		if result == nil {
			result = map[string]interface{}{"id": attempt.InstanceID, "title": attempt.Title}
		}
		if attempt.Skipped {
			result["success"] = true
			result["skipped"] = true
			result["reason"] = attempt.SkipReason
		}
		if attempt.AuthDeath {
			result["auth_death"] = true
		}
		ordered = append(ordered, result)
	}
	return ordered
}

func restartAllSessionsJSONPayload(total int, sweepResult session.BootSweepResult, sessions []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"success":      restartAllSessionsExitCode(sweepResult) == 0,
		"total":        total,
		"restarted":    sweepResult.Booted,
		"failed":       sweepResult.Failed,
		"skipped_auth": sweepResult.SkippedHeld,
		"auth_deaths":  sweepResult.AuthDeaths,
		"abandoned":    sweepResult.Abandoned,
		"auth_tripped": sweepResult.Tripped,
		"trip_message": sweepResult.TripMessage,
		"sessions":     sessions,
	}
}

func restartAllSessionsExitCode(sweepResult session.BootSweepResult) int {
	if sweepResult.Failed > 0 || sweepResult.Tripped {
		return 1
	}
	return 0
}
