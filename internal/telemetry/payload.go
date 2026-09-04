package telemetry

import (
	"encoding/hex"
	"encoding/json"
	"regexp"
	"runtime"
	"time"
)

// Payload is the exact wire format. Field set is fixed; see TELEMETRY.md.
// There is deliberately no hostname, username, path, title, prompt, IP, or
// sub-day timestamp anywhere in this struct.
type Payload struct {
	SchemaVersion int            `json:"schema_version"`
	InstallID     string         `json:"install_id"`
	Version       string         `json:"version"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
	Day           string         `json:"day"`
	Counters      map[string]int `json:"counters"`
}

// PayloadKeys lists the top-level JSON keys, for --help and for the test
// that asserts nothing else ever appears.
var PayloadKeys = []string{"schema_version", "install_id", "version", "os", "arch", "day", "counters"}

const maxCounter = 1000

var releaseVersion = regexp.MustCompile(`^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

func safeVersion(version string) string {
	if releaseVersion.MatchString(version) {
		return version
	}
	return "dev"
}

func validInstallID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// BuildPayload assembles the payload from state.
func BuildPayload(s *State, version string, now time.Time) Payload {
	counters := map[string]int{}
	for k, v := range s.Counters {
		if allowedKey(k) && v > 0 {
			counters[k] = min(v, maxCounter)
		}
	}
	return Payload{
		SchemaVersion: SchemaVersion,
		InstallID:     s.InstallID,
		Version:       safeVersion(version),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Day:           dayOf(now),
		Counters:      counters,
	}
}

// Marshal encodes the payload compactly.
func (p Payload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}
