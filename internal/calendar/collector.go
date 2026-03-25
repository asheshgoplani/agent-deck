package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"golang.org/x/oauth2"
)

const defaultBaseURL = "https://www.googleapis.com"

// Collector fetches upcoming events from Google Calendar via REST API.
type Collector struct {
	client      *http.Client
	baseURL     string // overridden in tests
	calendarIDs []string
	lookahead   time.Duration
}

// NewCollector creates a Collector with the given authenticated HTTP client.
// If calendarIDs is empty, only "primary" is queried.
func NewCollector(client *http.Client, calendarIDs []string, lookahead time.Duration) *Collector {
	if len(calendarIDs) == 0 {
		calendarIDs = []string{"primary"}
	}
	if lookahead == 0 {
		lookahead = 2 * time.Hour
	}
	return &Collector{
		client:      client,
		baseURL:     defaultBaseURL,
		calendarIDs: calendarIDs,
		lookahead:   lookahead,
	}
}

// NewCollectorFromConfig creates a Collector by loading credentials and token from disk.
// Uses primitive parameters to avoid an import cycle with internal/session.
// Call this from callers that have already extracted config values.
func NewCollectorFromConfig(credentialsPath, tokenPath string, calendarIDs []string, lookahead time.Duration) (*Collector, error) {
	oauthCfg, err := parseCredentials(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}

	tok, err := loadToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("no token found — run 'agent-deck google-calendar auth' first")
	}

	// TokenSource handles automatic refresh
	ts := oauthCfg.TokenSource(context.Background(), tok)
	client := oauth2.NewClient(context.Background(), ts)

	// Persist refreshed tokens
	newTok, err := ts.Token()
	if err == nil && newTok.AccessToken != tok.AccessToken {
		_ = saveToken(tokenPath, newTok)
	}

	return NewCollector(client, calendarIDs, lookahead), nil
}

// Collect fetches upcoming timed events across all configured calendars.
// Returns events sorted by start time. All-day and cancelled events are excluded.
func (c *Collector) Collect() ([]Event, error) {
	now := time.Now()
	timeMin := now.Format(time.RFC3339)
	timeMax := now.Add(c.lookahead).Format(time.RFC3339)

	var all []Event
	for _, calID := range c.calendarIDs {
		events, err := c.fetchEvents(calID, timeMin, timeMax)
		if err != nil {
			return nil, fmt.Errorf("calendar %s: %w", calID, err)
		}
		all = append(all, events...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].StartsAt.Before(all[j].StartsAt)
	})
	return all, nil
}

func (c *Collector) fetchEvents(calendarID, timeMin, timeMax string) ([]Event, error) {
	u := fmt.Sprintf("%s/calendar/v3/calendars/%s/events", c.baseURL, url.PathEscape(calendarID))

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("timeMin", timeMin)
	q.Set("timeMax", timeMax)
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("fields", "items(summary,status,start,end,hangoutLink)")
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var body eventsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var events []Event
	for _, item := range body.Items {
		if item.Status == "cancelled" {
			continue
		}
		// Skip all-day events (no dateTime)
		if item.Start.DateTime == "" {
			continue
		}
		startTime, err := time.Parse(time.RFC3339, item.Start.DateTime)
		if err != nil {
			continue
		}
		var endTime time.Time
		if item.End.DateTime != "" {
			endTime, _ = time.Parse(time.RFC3339, item.End.DateTime)
		}
		title := item.Summary
		if title == "" {
			title = "Busy"
		}
		events = append(events, Event{
			Title:      title,
			StartsAt:   startTime,
			EndsAt:     endTime,
			HasVideo:   item.HangoutLink != "",
			CalendarID: calendarID,
		})
	}
	return events, nil
}
