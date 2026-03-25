package calendar

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector_Collect(t *testing.T) {
	now := time.Now()
	start1 := now.Add(10 * time.Minute).Format(time.RFC3339)
	end1 := now.Add(25 * time.Minute).Format(time.RFC3339)
	start2 := now.Add(90 * time.Minute).Format(time.RFC3339)
	end2 := now.Add(150 * time.Minute).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"items": [
				{
					"summary": "Standup",
					"status": "confirmed",
					"start": {"dateTime": "` + start1 + `"},
					"end": {"dateTime": "` + end1 + `"},
					"hangoutLink": "https://meet.google.com/abc"
				},
				{
					"summary": "Planning",
					"status": "confirmed",
					"start": {"dateTime": "` + start2 + `"},
					"end": {"dateTime": "` + end2 + `"}
				},
				{
					"summary": "Cancelled",
					"status": "cancelled",
					"start": {"dateTime": "` + start1 + `"},
					"end": {"dateTime": "` + end1 + `"}
				}
			]
		}`))
	}))
	defer srv.Close()

	c := &Collector{
		client:      srv.Client(),
		baseURL:     srv.URL,
		calendarIDs: []string{"primary"},
		lookahead:   2 * time.Hour,
	}

	events, err := c.Collect()
	require.NoError(t, err)
	assert.Len(t, events, 2) // cancelled event excluded
	assert.Equal(t, "Standup", events[0].Title)
	assert.True(t, events[0].HasVideo)
	assert.Equal(t, "Planning", events[1].Title)
	assert.False(t, events[1].HasVideo)
}

func TestCollector_Collect_AllDayEventsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"items": [
				{
					"summary": "Holiday",
					"status": "confirmed",
					"start": {"date": "2026-03-25"},
					"end": {"date": "2026-03-26"}
				}
			]
		}`))
	}))
	defer srv.Close()

	c := &Collector{
		client:      srv.Client(),
		baseURL:     srv.URL,
		calendarIDs: []string{"primary"},
		lookahead:   2 * time.Hour,
	}

	events, err := c.Collect()
	require.NoError(t, err)
	assert.Empty(t, events) // all-day events excluded
}

func TestCollector_Collect_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Collector{
		client:      srv.Client(),
		baseURL:     srv.URL,
		calendarIDs: []string{"primary"},
		lookahead:   2 * time.Hour,
	}

	_, err := c.Collect()
	assert.Error(t, err)
}

func TestCollector_Collect_MultipleCalendars(t *testing.T) {
	now := time.Now()
	start1 := now.Add(20 * time.Minute).Format(time.RFC3339)
	start2 := now.Add(10 * time.Minute).Format(time.RFC3339)

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			w.Write([]byte(`{"items": [{"summary": "Work sync", "status": "confirmed", "start": {"dateTime": "` + start1 + `"}, "end": {"dateTime": "` + start1 + `"}}]}`))
		} else {
			w.Write([]byte(`{"items": [{"summary": "Personal", "status": "confirmed", "start": {"dateTime": "` + start2 + `"}, "end": {"dateTime": "` + start2 + `"}}]}`))
		}
	}))
	defer srv.Close()

	c := &Collector{
		client:      srv.Client(),
		baseURL:     srv.URL,
		calendarIDs: []string{"work@co.com", "me@gmail.com"},
		lookahead:   2 * time.Hour,
	}

	events, err := c.Collect()
	require.NoError(t, err)
	assert.Len(t, events, 2)
	// Sorted by start time — Personal (10m) before Work sync (20m)
	assert.Equal(t, "Personal", events[0].Title)
	assert.Equal(t, "Work sync", events[1].Title)
}
