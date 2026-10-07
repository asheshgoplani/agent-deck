package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

func webTelemetryGranted(t *testing.T) {
	t.Helper()
	telemetry.EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack, telemetry.EnvPostHogKey} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	telemetry.ClearCIForTest(t)
	telemetry.SetConfigDisabled(false)
	telemetry.SetTerminalForTest(t, true)
	st := telemetry.LoadState()
	if err := telemetry.Grant(st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

func webSpooledErrors(t *testing.T) []map[string]any {
	t.Helper()
	p, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(p), telemetry.SpoolFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			E string         `json:"e"`
			P map[string]any `json:"p"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.E == "error" {
			out = append(out, l.P)
		}
	}
	return out
}

// A web handler that answers 5xx reports error area=web; the message is
// never part of the event.
func TestWebServerErrorRecordsErrorEvent(t *testing.T) {
	webTelemetryGranted(t)
	writeAPIError(httptest.NewRecorder(), http.StatusInternalServerError, "INTERNAL_ERROR", "failed to save /secret/path")
	errs := webSpooledErrors(t)
	if len(errs) != 1 || errs[0]["area"] != "web" || errs[0]["kind"] != "other" {
		t.Fatalf("error events = %v, want one area=web kind=other", errs)
	}
}

// A 4xx answer is the caller's mistake and a 503 is a feature that is not
// configured; neither is an agent-deck failure.
func TestWebClientErrorRecordsNothing(t *testing.T) {
	webTelemetryGranted(t)
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable} {
		writeAPIError(httptest.NewRecorder(), code, "NOT_FOUND", "x")
	}
	if errs := webSpooledErrors(t); len(errs) != 0 {
		t.Fatalf("4xx/503 answers spooled error events: %v", errs)
	}
}
