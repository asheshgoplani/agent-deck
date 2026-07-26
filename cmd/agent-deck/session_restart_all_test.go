package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRestartAllSessionsJSONPayload_reportsSkippedAuthAndTrip(t *testing.T) {
	results := map[string]map[string]interface{}{
		"booted": {"id": "booted", "title": "booted", "success": true},
	}
	sweepResult := session.BootSweepResult{
		Attempts: []session.BootAttempt{
			{InstanceID: "held", Title: "held", Skipped: true, SkipReason: "held: run /login"},
			{InstanceID: "booted", Title: "booted", AuthDeath: true},
			{InstanceID: "left", Title: "left", Skipped: true, SkipReason: "circuit tripped: preceding boots died on authentication"},
		},
		Booted:      1,
		SkippedHeld: 1,
		AuthDeaths:  1,
		Abandoned:   1,
		Tripped:     true,
		TripMessage: "STOPPED after auth deaths",
	}

	sessions := restartAllSessionRecords(results, sweepResult.Attempts)
	payload := restartAllSessionsJSONPayload(3, sweepResult, sessions)

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got struct {
		Success     bool `json:"success"`
		Total       int  `json:"total"`
		Restarted   int  `json:"restarted"`
		SkippedAuth int  `json:"skipped_auth"`
		AuthDeaths  int  `json:"auth_deaths"`
		Abandoned   int  `json:"abandoned"`
		AuthTripped bool `json:"auth_tripped"`
		Sessions    []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Success   bool   `json:"success"`
			Skipped   bool   `json:"skipped"`
			Reason    string `json:"reason"`
			AuthDeath bool   `json:"auth_death"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal payload: %v\n%s", err, encoded)
	}

	if got.Success {
		t.Fatal("tripped restart-all JSON must report success=false")
	}
	if got.Total != 3 || got.Restarted != 1 || got.SkippedAuth != 1 || got.AuthDeaths != 1 || got.Abandoned != 1 || !got.AuthTripped {
		t.Fatalf("summary counters did not round-trip: %+v", got)
	}
	if len(got.Sessions) != 3 {
		t.Fatalf("sessions length = %d, want 3", len(got.Sessions))
	}
	if got.Sessions[0].ID != "held" || !got.Sessions[0].Success || !got.Sessions[0].Skipped || got.Sessions[0].Reason != "held: run /login" {
		t.Fatalf("held skip record missing machine-readable reason: %+v", got.Sessions[0])
	}
	if got.Sessions[1].ID != "booted" || !got.Sessions[1].Success || !got.Sessions[1].AuthDeath {
		t.Fatalf("auth-death record lost existing boot result: %+v", got.Sessions[1])
	}
	if got.Sessions[2].ID != "left" || !got.Sessions[2].Success || !got.Sessions[2].Skipped || got.Sessions[2].Reason == "" {
		t.Fatalf("abandoned skip record missing reason: %+v", got.Sessions[2])
	}
}

func TestRestartAllSessionsExitCode_reflectsFailuresAndAuthTrips(t *testing.T) {
	tests := []struct {
		name        string
		sweepResult session.BootSweepResult
		want        int
	}{
		{name: "all restarts clean", sweepResult: session.BootSweepResult{Booted: 2}, want: 0},
		{name: "plain restart failure", sweepResult: session.BootSweepResult{Failed: 1}, want: 1},
		{name: "auth circuit tripped", sweepResult: session.BootSweepResult{Tripped: true}, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restartAllSessionsExitCode(tt.sweepResult)
			if got != tt.want {
				t.Fatalf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}
