package triggers

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// BusKind is the event bus kind every trigger change publishes, so the TUI
// and the Mac app update live (`events follow --kind session.trigger`).
const BusKind = "session.trigger"

// DefaultMute is how long `triggers mute` lasts without --for.
const DefaultMute = 24 * time.Hour

// Fire is one send as the CLI saw it.
type Fire struct {
	Source        Source
	Target        string // as written on the command line
	TargetProfile string
	SessionID     string // "" when the target resolved to no session
	Message       string
	Outcome       string
	Verb          string
}

// Publish sends one session.trigger frame; failures are ignored (the bus
// is best-effort and the tables are the source of truth).
func Publish(profile, sessionID string, data map[string]string) {
	events.PublishProfile(profile, BusKind, sessionID, data)
}

// Record stores one fire: the trigger row (created on its first fire), the
// fire log entry with only a redacted first line and a hash of the text,
// and a bus frame.
func Record(db *statedb.StateDB, profile, host string, f Fire) error {
	if db == nil {
		return nil
	}
	by := "title"
	if sessionIDRe.MatchString(f.Target) {
		by = "id"
	}
	verb := f.Verb
	if verb == "" {
		verb = "send"
	}
	id := f.Source.ID()
	if err := db.UpsertPushedTrigger(statedb.TriggerRow{
		ID: id, Kind: f.Source.Kind, Source: f.Source.Name, Host: host, Verb: verb,
		Target: f.Target, TargetBy: by, TargetProfile: f.TargetProfile, Inferred: f.Source.Inferred,
	}); err != nil {
		return err
	}
	if _, err := db.RecordTriggerFire(statedb.TriggerFireRow{
		TriggerID: id, Target: f.Target, TargetSessionID: f.SessionID, Outcome: f.Outcome,
		Line: FirstLine(f.Message), TextHash: TextHash(f.Message),
	}); err != nil {
		return err
	}
	Publish(profile, f.SessionID, map[string]string{"trigger_id": id, "source": f.Source.Name, "kind": f.Source.Kind, "outcome": f.Outcome, "health": HealthAfter(f.Outcome)})
	return nil
}

// HealthAfter is the health a fire with this outcome leaves its trigger in,
// for the bus frame (the list recomputes it from the stored rows).
func HealthAfter(outcome string) string {
	switch outcome {
	case OutcomeDeadTarget:
		return HealthDeadTarget
	case OutcomeFailed:
		return HealthFailing
	}
	return HealthOK
}

// MutedUntil returns when the trigger's mute ends (zero when not muted now).
func MutedUntil(db *statedb.StateDB, id string, now time.Time) time.Time {
	if db == nil {
		return time.Time{}
	}
	row, err := db.GetTrigger(id)
	if err != nil || row == nil || !row.MutedUntil.After(now) {
		return time.Time{}
	}
	return row.MutedUntil
}

// ApplyScan stores a scan result for this host.
func ApplyScan(db *statedb.StateDB, profile, host string, found []Found) error {
	rows := make([]statedb.TriggerRow, 0, len(found))
	for _, f := range found {
		rows = append(rows, statedb.TriggerRow{
			ID: f.ID(), Kind: f.TriggerKind(), Source: f.Name(), Host: host, ConfigPath: f.ConfigPath, Verb: f.Verb,
			Target: f.Target, TargetBy: f.TargetBy, TargetProfile: f.TargetProfile, Schedule: f.Schedule,
			Unit: f.Unit, UnitKind: f.Kind,
		})
	}
	if err := db.ReplaceScannedTriggers(rows); err != nil {
		return err
	}
	if err := db.SetMeta(scanStampKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return err
	}
	if err := db.BumpTriggersRev(); err != nil {
		return err
	}
	Publish(profile, "", map[string]string{"scan": strconv.Itoa(len(found))})
	return nil
}

const scanStampKey = "triggers_scanned_at"

// ScanStale reports whether the last scan is older than maxAge (or never ran).
func ScanStale(db *statedb.StateDB, maxAge time.Duration, now time.Time) bool {
	v, err := db.GetMeta(scanStampKey)
	if err != nil || v == "" {
		return true
	}
	ts, err := strconv.ParseInt(v, 10, 64)
	return err != nil || now.Sub(time.Unix(ts, 0)) > maxAge
}

// Load reads every trigger of this profile's database as views.
func Load(db *statedb.StateDB, resolve Resolver, host string, now time.Time) ([]View, error) {
	rows, err := db.ListTriggers()
	if err != nil {
		return nil, err
	}
	stats, err := db.TriggerStatsSince(now.Add(-24 * time.Hour))
	if err != nil {
		return nil, err
	}
	return Build(rows, stats, resolve, host, now), nil
}

// SetMute mutes (until > now) or unmutes (zero) a trigger and publishes it.
func SetMute(db *statedb.StateDB, profile, id string, until time.Time) error {
	ok, err := db.SetTriggerMute(id, until)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no trigger %q (see agent-deck triggers --all)", id)
	}
	state := "unmuted"
	if !until.IsZero() {
		state = "muted"
	}
	Publish(profile, "", map[string]string{"trigger_id": id, "mute": state})
	return nil
}

// RunCommand is the command `triggers run` executes for a trigger: start
// its unit once. It never stops, disables or pauses anything.
func RunCommand(v View, uid int) ([]string, error) {
	if !v.Runnable {
		return nil, fmt.Errorf("%s cannot be run from agent-deck: only scanned launchd jobs and systemd units can (kind %s)", v.ID, v.Kind)
	}
	unit := v.unit
	switch v.unitKind {
	case KindSystemd:
		return []string{"systemctl", "--user", "start", unit + ".service"}, nil
	case KindLaunchd:
		return []string{"launchctl", "kickstart", "gui/" + strconv.Itoa(uid) + "/" + unit}, nil
	}
	return nil, fmt.Errorf("%s: unsupported kind %s", v.ID, v.Kind)
}

// Hostname is the display name of this host in the view.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
