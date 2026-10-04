package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// EnvTelemetryOwner suppresses only install.tick, never grants consent.
const EnvTelemetryOwner = "AGENTDECK_TELEMETRY_OWNER"
const installTickFileName = "telemetry-tick.json"
const installTickTimeout = 2 * time.Second

var configOwner bool

// SetConfigOwner records [telemetry].owner at process startup.
func SetConfigOwner(owner bool) { configOwner = owner }

// installTick is separate from State: old binaries discard unknown State
// fields. Keep this ledger across disable and reset-id to retain the day's
// nonce even after a lost acknowledgment. It contains no install identity.
type installTick struct {
	Day         string `json:"day"`
	TickID      string `json:"tick_id"`
	Version     string `json:"v"`
	Sent        bool   `json:"sent"`
	LastSentDay string `json:"last_sent_day,omitempty"`
}

// InstallTickStatus describes durable acknowledgments, not merely attempts.
type InstallTickStatus struct {
	Event       string `json:"event"`
	State       string `json:"state"`
	Day         string `json:"day,omitempty"`
	LastSentDay string `json:"last_sent_day,omitempty"`
	Owner       bool   `json:"owner"`
}

// ReadInstallTickStatus never writes state or generates a nonce.
func ReadInstallTickStatus() InstallTickStatus {
	st := InstallTickStatus{Event: "install.tick", State: "never", Owner: tickOwner()}
	tick, err := readInstallTick()
	if err != nil {
		st.State = "unavailable"
		return st
	}
	st.Day, st.LastSentDay = tick.Day, tick.LastSentDay
	if tick.Day != "" {
		st.State = "pending"
		if tick.Sent {
			st.State = "sent"
		}
	}
	return st
}

// Summary is shared by CLI status and TUI Settings.
func (s InstallTickStatus) Summary() string {
	last := s.LastSentDay
	if last == "" {
		last = "never"
	}
	if s.State == "unavailable" {
		last = "unknown"
	}
	state := s.State
	if s.Owner {
		state = "owner suppressed"
	}
	return "install.tick: " + state + "; last sent " + last
}

func tickOwner() bool { return configOwner || isTruthy(os.Getenv(EnvTelemetryOwner)) }

func readInstallTick() (installTick, error) {
	path, err := siblingPath(installTickFileName)
	if err != nil {
		return installTick{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return installTick{}, nil
	}
	if err != nil {
		return installTick{}, err
	}
	if !info.Mode().IsRegular() {
		return installTick{}, errors.New("telemetry: tick ledger is not a regular file")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return installTick{}, nil
	}
	if err != nil {
		return installTick{}, err
	}
	var tick installTick
	if err := json.Unmarshal(data, &tick); err != nil {
		return tick, err
	}
	if !validTickDay(tick.Day) || !validTickID(tick.TickID) || safeVersion(tick.Version) == "dev" ||
		(tick.LastSentDay != "" && (!validTickDay(tick.LastSentDay) || tick.LastSentDay > tick.Day)) ||
		(tick.Sent && tick.LastSentDay != tick.Day) {
		return tick, errors.New("telemetry: invalid tick ledger; refusing to replace its nonce")
	}
	return tick, nil
}

func validTickDay(day string) bool {
	parsed, err := time.Parse(DayFormat, day)
	return err == nil && parsed.Format(DayFormat) == day
}

// tickID formats all 128 random bits as a UUID without replacing entropy
// bits with a UUID version/variant. PostHog receives the same event nonce.
func tickID(h string) string {
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func validTickID(id string) bool {
	h := strings.ReplaceAll(id, "-", "")
	return validInstallID(h) && id == tickID(h)
}

func saveInstallTick(tick installTick) error {
	path, err := siblingPath(installTickFileName)
	if err != nil {
		return err
	}
	data, err := json.Marshal(tick)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFileDurable(path, data, 0600); err != nil {
		return err
	}
	return syncInstallTickDirectory(filepath.Dir(path))
}

func tickBody(tick installTick) ([]byte, error) {
	// PostHog requires a distinct_id. Use only this day's random event nonce,
	// never State.InstallID. It cannot join days or detailed telemetry.
	return json.Marshal(phBatch{APIKey: redactedAPIKey, Batch: []phEvent{{ //nolint:gosec // G117: only the redacted placeholder; post() inserts the key
		Event: "install.tick", UUID: tick.TickID, DistinctID: tick.TickID, Timestamp: tick.Day + "T12:00:00Z",
		Properties: map[string]any{"day": tick.Day, "v": tick.Version, "consent_state": string(ConsentGranted), "tick_id": tick.TickID,
			"$geoip_disable": true, "$process_person_profile": false},
	}}})
}

// MaybeInstallTick is called by the TUI's background startup/hourly upload
// command. It preserves the existing human-TUI and consent-day rules. A
// later check retries failures with the durable daily nonce; dashboard
// DISTINCT tick_id counting makes lost acknowledgments harmless.
func MaybeInstallTick(ctx context.Context) UploadResult { return maybeInstallTick(ctx, post) }

func maybeInstallTick(ctx context.Context, send func(context.Context, []byte, time.Duration) postResult) UploadResult {
	if reason := uploadGate(); reason != "" {
		return UploadResult{Reason: reason}
	}
	if tickOwner() || LogMode() {
		return UploadResult{Reason: "owner or log mode"}
	}
	// Do not block the UI behind another process's upload. That process, or
	// a later startup/hourly check, can send this day's tick.
	unlock, err := lockStateWithFlags(syscall.LOCK_EX | syscall.LOCK_NB)
	if err != nil {
		return UploadResult{Reason: "telemetry lock unavailable"}
	}
	defer unlock()
	s := LoadState()
	if ok, reason := Enabled(s); !ok {
		return UploadResult{Reason: string(reason)}
	}
	today := dayOf(nowFn())
	if s.ConsentDay >= today {
		return UploadResult{Reason: "nothing is sent on the consent day"}
	}
	tick, err := readInstallTick()
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	if tick.Day > today || (tick.Day == today && tick.Sent) {
		return UploadResult{Reason: "day already handled"}
	}
	if tick.Day != today {
		nonce, err := randomHex(16)
		if err != nil {
			return UploadResult{Reason: err.Error()}
		}
		tick = installTick{Day: today, TickID: tickID(nonce), Version: safeVersion(processVersion), LastSentDay: tick.LastSentDay}
	}
	// Confirm the nonce is durable on every attempt, including when a previous
	// reservation renamed successfully but its directory sync failed.
	if err := saveInstallTick(tick); err != nil {
		return UploadResult{Reason: err.Error()}
	}
	body, err := tickBody(tick)
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	if reason := uploadGate(); reason != "" {
		return UploadResult{Reason: reason}
	}
	if tickOwner() || LogMode() {
		return UploadResult{Reason: "owner or log mode"}
	}
	ctx, cancel := context.WithTimeout(ctx, installTickTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return UploadResult{Reason: err.Error()}
	}
	result := send(ctx, body, installTickTimeout)
	if result.err != nil {
		return UploadResult{Attempted: true, Reason: "tick not acknowledged; will retry"}
	}
	tick.Sent, tick.LastSentDay = true, tick.Day
	res := UploadResult{Attempted: true, Sent: true, Events: 1}
	if err := saveInstallTick(tick); err != nil {
		res.Reason = "tick acknowledged; could not persist acknowledgment"
	}
	return res
}

// syncInstallTickDirectory is a test seam for storage errors. Unlike the
// general atomicfile helper, a tick reservation requires directory durability.
var syncInstallTickDirectory = func(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
