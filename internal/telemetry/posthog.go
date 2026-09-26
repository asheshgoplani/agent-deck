package telemetry

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// PostHog batch limits per request and per upload.
const (
	maxBatchEvents   = 500
	maxBatchBytes    = 256 << 10
	maxBatchRequests = 5
)

// phEvent is one event in PostHog's /batch/ body. Personless, no GeoIP, no
// IP, and no $set/$groups/$session_id/$current_url properties, ever.
type phEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	UUID       string         `json:"uuid"`
	Timestamp  string         `json:"timestamp"`
	Properties map[string]any `json:"properties"`
}

type phBatch struct {
	APIKey string    `json:"api_key"`
	Batch  []phEvent `json:"batch"`
}

// redactedAPIKey stands in for the project key in every body agent-deck
// builds, stores or prints (preview, show-last, log mode); only post()
// swaps the real key in, for the request itself.
const redactedAPIKey = "phc_redacted" //nolint:gosec // G101: a placeholder, not a credential

// redactedBodyPrefix is how every encoded batch body starts.
var redactedBodyPrefix = []byte(`{"api_key":"` + redactedAPIKey + `",`)

// withAPIKey returns body with the placeholder replaced by the real key.
func withAPIKey(body []byte) ([]byte, error) {
	key, ok := PostHogKey()
	if !ok {
		return nil, errors.New("telemetry: no valid PostHog project key")
	}
	if !bytes.HasPrefix(body, redactedBodyPrefix) {
		return nil, errors.New("telemetry: request body is not a redacted batch")
	}
	k, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	rest := body[len(redactedBodyPrefix)-1:] // from the comma on
	out := make([]byte, 0, len(`{"api_key":`)+len(k)+len(rest))
	out = append(append(append(out, `{"api_key":`...), k...), rest...)
	return out, nil
}

// pendingEvent is one event ready to encode, with where it came from.
type pendingEvent struct {
	ev        phEvent
	spoolUUID string // spool line uuid, "" for a rollup
	rollupDay string // rollup day, "" for a spool line
}

// toPostHog maps a spool line to a PostHog event (TELEMETRY.md "Wire format").
// The timestamp is local wall-clock time labelled UTC ("floating" time): it
// leaks no timezone and PostHog's hour/weekday breakdowns show local time.
func (s *State) toPostHog(l spoolLine) phEvent {
	props := map[string]any{
		"$process_person_profile": false,
		"$geoip_disable":          true,
		"$lib":                    "agent-deck",
		"schema":                  SchemaVersion,
		"v":                       l.V,
		"os":                      oneOf(runtime.GOOS, osValues),
		"arch":                    oneOf(runtime.GOARCH, archValues),
		"day":                     l.D,
		"seq":                     l.S,
		"actor":                   l.A,
		"surface":                 l.SF,
		"level":                   l.L,
		"pre_v2":                  s.PreV2,
	}
	props["install_age"], props["install_week"] = s.installAge(l.D)
	hour := 12
	if l.H != nil && l.W != nil {
		hour = *l.H
		props["hour_local"] = *l.H
		props["weekday_local"] = *l.W
	}
	for k, v := range l.P {
		props[k] = v
	}
	return phEvent{
		Event: l.E, DistinctID: s.InstallID, UUID: l.U,
		Timestamp:  fmt.Sprintf("%sT%02d:00:00Z", l.D, hour),
		Properties: props,
	}
}

// rollupUUID is deterministic per (install salt, day, event, key), so a
// rebuilt rollup after a lost acknowledgement is deduplicated by PostHog.
func (s *State) rollupUUID(day, name, key string) string {
	m := hmac.New(sha256.New, []byte(s.Salt))
	m.Write([]byte(day + "\x00" + name + "\x00" + key))
	return formatUUID(hex.EncodeToString(m.Sum(nil))[:32])
}

// pending builds every event that is ready to upload at now: spool lines of
// completed local hours (completed days at level basic, whose lines carry no
// hour) and rollups of completed local days. It advances s.Seq for rollups.
func (s *State) pending(lines []spoolLine, now time.Time) []pendingEvent {
	today, hour := dayOf(now), now.Local().Hour()
	var out []pendingEvent
	for _, l := range lines {
		done := l.D < today || l.D == today && l.H != nil && *l.H < hour
		if done {
			out = append(out, pendingEvent{ev: s.toPostHog(l), spoolUUID: l.U})
		}
	}
	level := EffectiveLevel(s)
	for _, day := range sortedKeys(s.Daily) {
		if day >= today {
			continue
		}
		for _, r := range rollupEvents(s.Daily[day], level) {
			if Validate(r.name, r.props) != nil {
				continue
			}
			s.Seq++
			l := spoolLine{E: r.name, U: s.rollupUUID(day, r.name, r.key), D: day, S: s.Seq,
				V: safeVersion(processVersion), A: "human", SF: string(SurfaceTUI), L: string(level), P: r.props}
			out = append(out, pendingEvent{ev: s.toPostHog(l), rollupDay: day})
		}
		out = append(out, pendingEvent{rollupDay: day}) // marks the day for removal even when empty
	}
	return out
}

// chunk splits events into request bodies within the per-request limits.
func chunk(events []pendingEvent) ([][]byte, [][]pendingEvent) {
	var bodies [][]byte
	var groups [][]pendingEvent
	var cur []pendingEvent
	size := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		b := phBatch{APIKey: redactedAPIKey}
		for _, p := range cur {
			if p.ev.Event != "" {
				b.Batch = append(b.Batch, p.ev)
			}
		}
		body, err := json.Marshal(b) //nolint:gosec // G117: only the redacted placeholder; post() inserts the key
		if err == nil {
			bodies = append(bodies, body)
			groups = append(groups, cur)
		}
		cur, size = nil, 0
	}
	for _, p := range events {
		n := 0
		if p.ev.Event != "" {
			data, err := json.Marshal(p.ev)
			if err != nil {
				continue
			}
			n = len(data) + 1
		}
		if len(cur) >= maxBatchEvents || size+n > maxBatchBytes-256 {
			flush()
		}
		cur = append(cur, p)
		size += n
	}
	flush()
	return bodies, groups
}
