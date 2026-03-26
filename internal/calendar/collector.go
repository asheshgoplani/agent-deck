package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
		return nil, fmt.Errorf("no token found (%w) — run 'agent-deck google-calendar auth' first", err)
	}

	// TokenSource handles automatic refresh. Wrap it in a persisting source so
	// every token rotation during the process lifetime is written to disk.
	rawTS := oauthCfg.TokenSource(context.Background(), tok)
	ts := &persistingTokenSource{inner: rawTS, tokenPath: tokenPath, last: tok.AccessToken}

	// Eagerly validate the token. Fail fast if auth is broken so callers get a
	// clear error rather than opaque HTTP 401s on first Collect.
	if _, err := ts.Token(); err != nil {
		return nil, fmt.Errorf("token unavailable — run 'agent-deck google-calendar auth' to re-authorize: %w", err)
	}

	client := oauth2.NewClient(context.Background(), ts)
	return NewCollector(client, calendarIDs, lookahead), nil
}

// Collect fetches upcoming timed events across all configured calendars.
// Returns events sorted by start time. All-day and cancelled events are excluded.
// The context is forwarded to each HTTP request; cancellation aborts in-flight calls.
func (c *Collector) Collect(ctx context.Context) ([]Event, error) {
	now := time.Now()
	timeMin := now.Format(time.RFC3339)
	timeMax := now.Add(c.lookahead).Format(time.RFC3339)

	var all []Event
	for _, calID := range c.calendarIDs {
		events, err := c.fetchEvents(ctx, calID, timeMin, timeMax)
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

func (c *Collector) fetchEvents(ctx context.Context, calendarID, timeMin, timeMax string) ([]Event, error) {
	u := fmt.Sprintf("%s/calendar/v3/calendars/%s/events", c.baseURL, url.PathEscape(calendarID))

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("timeMin", timeMin)
	q.Set("timeMax", timeMax)
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("fields", "items(summary,status,start,end)")
	req.URL.RawQuery = q.Encode()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := readErrorMessage(resp)
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("HTTP 401: %s — run 'agent-deck google-calendar auth' to re-authorize", msg)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
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
			slog.Warn("calendar: skipping event with unparseable start time",
				"summary", item.Summary, "dateTime", item.Start.DateTime, "error", err)
			continue
		}
		var endTime time.Time
		if item.End.DateTime != "" {
			var endErr error
			endTime, endErr = time.Parse(time.RFC3339, item.End.DateTime)
			if endErr != nil {
				slog.Warn("calendar: unparseable end time, using zero",
					"summary", item.Summary, "dateTime", item.End.DateTime, "error", endErr)
			}
		}
		title := item.Summary
		if title == "" {
			title = "Busy"
		}
		events = append(events, Event{
			Title:      title,
			StartsAt:   startTime,
			EndsAt:     endTime,
			CalendarID: calendarID,
		})
	}
	return events, nil
}

// readErrorMessage extracts the human-readable message from a Google API error
// response body. Returns the raw status text if the body is absent or unparseable.
func readErrorMessage(resp *http.Response) string {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil || len(body) == 0 {
		return resp.Status
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return resp.Status
}
