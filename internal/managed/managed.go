// Package managed reads the organization-managed configuration that a
// company installs next to agent-deck (an MDM profile or a pkg drops the
// file; the person does not edit it). It holds only settings a company may
// decide for its machines: an opaque org id, a telemetry level ceiling, a
// crash-report DSN and an update channel. It can lower telemetry or turn it
// off; it can never grant consent or raise a level above what the person
// chose (TELEMETRY.md "Managed configuration").
package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// EnvPath overrides the managed file path (tests, and installs that keep
// the file elsewhere). Set to an empty value it disables the default path.
const EnvPath = "AGENTDECK_MANAGED_CONFIG"

// maxFileBytes bounds what Load reads; the real file is a few hundred bytes.
const maxFileBytes = 64 << 10

// Telemetry levels a managed file may set. "full" is no ceiling.
const (
	LevelOff   = "off"
	LevelBasic = "basic"
	LevelFull  = "full"
)

// Update channels a managed file may set.
const (
	ChannelStable = "stable"
	ChannelPilot  = "pilot"
)

// DefaultPath is the per-OS system location of the managed file, or "" on
// systems without one.
func DefaultPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/AgentDeck/managed.json"
	case "linux":
		return "/etc/agent-deck/managed.json"
	}
	return ""
}

// Path returns the managed file path: AGENTDECK_MANAGED_CONFIG when set
// (even to ""), else DefaultPath.
func Path() string {
	if v, ok := os.LookupEnv(EnvPath); ok {
		return strings.TrimSpace(v)
	}
	return DefaultPath()
}

// Config is the managed file (JSON). Unknown keys are ignored so an older
// agent-deck reads a newer file.
type Config struct {
	OrgID          string `json:"org_id,omitempty"`
	TelemetryLevel string `json:"telemetry_level,omitempty"`
	CrashDSN       string `json:"crash_dsn,omitempty"`
	UpdateChannel  string `json:"update_channel,omitempty"`
}

// Result is what Load found. Present is false when there is no file. Err is
// set when a file exists but cannot be read or fails validation; callers
// then fail closed (telemetry and crash reports off) and use none of it.
type Result struct {
	Config
	Path    string
	Present bool
	Err     error
}

// Load reads and validates the managed file at Path().
func Load() Result { return LoadFile(Path()) }

// LoadFile reads and validates the managed file at path.
func LoadFile(path string) Result {
	r := Result{Path: path}
	if path == "" {
		return r
	}
	f, err := os.Open(path) //nolint:gosec // G304: a fixed system path or the admin's override
	if errors.Is(err, os.ErrNotExist) {
		return r
	}
	r.Present = true
	if err != nil {
		r.Err = err
		return r
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		r.Err = err
		return r
	}
	if len(data) > maxFileBytes {
		r.Err = fmt.Errorf("managed config larger than %d bytes", maxFileBytes)
		return r
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		r.Err = fmt.Errorf("managed config is not valid JSON: %w", err)
		return r
	}
	c.OrgID = strings.TrimSpace(c.OrgID)
	c.TelemetryLevel = strings.ToLower(strings.TrimSpace(c.TelemetryLevel))
	c.CrashDSN = strings.TrimSpace(c.CrashDSN)
	c.UpdateChannel = strings.ToLower(strings.TrimSpace(c.UpdateChannel))
	if err := c.Validate(); err != nil {
		r.Err = err
		return r
	}
	r.Config = c
	return r
}

// Validate checks every set field. Error messages never quote the DSN.
func (c Config) Validate() error {
	if c.OrgID != "" && !ValidOrgID(c.OrgID) {
		return errors.New("managed config: org_id must be a short opaque slug (a-z, 0-9, -; 3-40 chars) or a UUID")
	}
	switch c.TelemetryLevel {
	case "", LevelOff, LevelBasic, LevelFull:
	default:
		return errors.New(`managed config: telemetry_level must be "off", "basic" or "full"`)
	}
	if c.CrashDSN != "" {
		if err := ValidateDSN(c.CrashDSN); err != nil {
			return fmt.Errorf("managed config: crash_dsn: %w", err)
		}
	}
	switch c.UpdateChannel {
	case "", ChannelStable, ChannelPilot:
	default:
		return errors.New(`managed config: update_channel must be "stable" or "pilot"`)
	}
	return nil
}

var (
	orgSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)
	orgUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidOrgID reports an opaque org id: a lowercase slug of 3-40 characters
// (letters, digits, inner dashes) or a lowercase UUID. Dots, "@", spaces and
// upper case are refused, so an email address, domain or person's name
// written with them cannot become an org id. A readable company name in a
// slug is still possible; the docs ask for a random one.
func ValidOrgID(id string) bool {
	return orgSlugPattern.MatchString(id) || orgUUIDPattern.MatchString(id)
}

var dsnProjectPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._-]+/)*[0-9]+/?$`)

// ValidateDSN checks the shape of a Sentry-style DSN,
// https://<public key>@<host>/<project id>, without a secret part. Plain
// http is accepted only for localhost (a local relay).
func ValidateDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return errors.New("not a URL")
	}
	host := u.Hostname()
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	switch {
	case u.Scheme != "https" && !(u.Scheme == "http" && local):
		return errors.New("must be https (http only for localhost)")
	case host == "":
		return errors.New("has no host")
	case u.User == nil || u.User.Username() == "":
		return errors.New("has no public key")
	case hasPassword(u):
		return errors.New("must not contain a secret key")
	case !dsnProjectPattern.MatchString(u.Path):
		return errors.New("has no project id")
	case u.RawQuery != "" || u.Fragment != "":
		return errors.New("must not have a query or fragment")
	}
	return nil
}

func hasPassword(u *url.URL) bool {
	_, ok := u.User.Password()
	return ok
}
