package calendar

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvent_TimeUntilLabel(t *testing.T) {
	tests := []struct {
		name     string
		startsAt time.Time
		want     string
	}{
		{"future_minutes", time.Now().Add(12 * time.Minute), "12m"},
		{"future_hours", time.Now().Add(90 * time.Minute), "1h30m"},
		{"past_event", time.Now().Add(-5 * time.Minute), "now"},
		{"imminent", time.Now().Add(30 * time.Second), "now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Title: "test", StartsAt: tt.startsAt}
			assert.Equal(t, tt.want, e.TimeUntilLabel())
		})
	}
}

func TestEvent_Urgency(t *testing.T) {
	tests := []struct {
		name     string
		startsAt time.Time
		want     Urgency
	}{
		{"critical", time.Now().Add(3 * time.Minute), UrgencyCritical},
		{"warning", time.Now().Add(10 * time.Minute), UrgencyWarning},
		{"normal", time.Now().Add(45 * time.Minute), UrgencyNormal},
		{"in_progress", time.Now().Add(-5 * time.Minute), UrgencyCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Title: "test", StartsAt: tt.startsAt}
			assert.Equal(t, tt.want, e.Urgency())
		})
	}
}

func TestParseEventsResponse(t *testing.T) {
	raw := `{
		"items": [
			{
				"summary": "Standup",
				"status": "confirmed",
				"start": {"dateTime": "2026-03-25T10:00:00-07:00"},
				"end": {"dateTime": "2026-03-25T10:15:00-07:00"}
				},
			{
				"summary": "All-day event",
				"status": "confirmed",
				"start": {"date": "2026-03-25"},
				"end": {"date": "2026-03-26"}
			}
		]
	}`
	var resp eventsListResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	assert.Len(t, resp.Items, 2)
	assert.Equal(t, "Standup", resp.Items[0].Summary)
	assert.Equal(t, "2026-03-25", resp.Items[1].Start.Date)
}
