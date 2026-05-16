package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// TestFlickerDetector_BelowThreshold_NoWarn verifies that a small number of
// transitions within the window does NOT emit a flicker_detected log.
func TestFlickerDetector_BelowThreshold_NoWarn(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug: true, LogDir: logDir, Level: "debug", Format: "json",
	})
	defer logging.Shutdown()

	d := NewFlickerDetector()
	now := time.Unix(1_700_000_000, 0)
	d.observeAt("inst-A", "running", now)
	d.observeAt("inst-A", "waiting", now.Add(20*time.Second))

	logging.Shutdown()
	body := readLogFile(t, logDir)
	if strings.Contains(body, `"flicker_detected"`) {
		t.Errorf("did not expect flicker_detected for 2 transitions in 60s; got:\n%s", body)
	}
}

// TestFlickerDetector_AboveThreshold_EmitsWarn verifies that >3 transitions
// in the 60s window emit exactly one flicker_detected WARN.
func TestFlickerDetector_AboveThreshold_EmitsWarn(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug: true, LogDir: logDir, Level: "debug", Format: "json",
	})
	defer logging.Shutdown()

	d := NewFlickerDetector()
	base := time.Unix(1_700_000_000, 0)
	d.observeAt("inst-A", "running", base)
	d.observeAt("inst-A", "waiting", base.Add(5*time.Second))
	d.observeAt("inst-A", "running", base.Add(10*time.Second))
	d.observeAt("inst-A", "waiting", base.Add(15*time.Second)) // 4th transition: WARN here

	logging.Shutdown()
	body := readLogFile(t, logDir)
	if !strings.Contains(body, `"flicker_detected"`) {
		t.Errorf("expected flicker_detected after 4 transitions in 15s; got:\n%s", body)
	}
	if !strings.Contains(body, `"level":"WARN"`) {
		t.Errorf("expected WARN level; got:\n%s", body)
	}
	if !strings.Contains(body, `"session":"inst-A"`) {
		t.Errorf("expected session attribute on log; got:\n%s", body)
	}
}

// TestFlickerDetector_SpreadOutsideWindow_NoWarn verifies that 4
// transitions spread over 5 minutes don't trigger flicker (each new
// transition prunes anything older than 60s before evaluating).
func TestFlickerDetector_SpreadOutsideWindow_NoWarn(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug: true, LogDir: logDir, Level: "debug", Format: "json",
	})
	defer logging.Shutdown()

	d := NewFlickerDetector()
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 4; i++ {
		d.observeAt("inst-A", "running", base.Add(time.Duration(i)*90*time.Second))
	}

	logging.Shutdown()
	body := readLogFile(t, logDir)
	if strings.Contains(body, `"flicker_detected"`) {
		t.Errorf("did not expect flicker for transitions spread across >60s windows; got:\n%s", body)
	}
}

// TestFlickerDetector_PerSessionIsolated verifies that a flicker on one
// session does not implicate another quiet session.
func TestFlickerDetector_PerSessionIsolated(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug: true, LogDir: logDir, Level: "debug", Format: "json",
	})
	defer logging.Shutdown()

	d := NewFlickerDetector()
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 4; i++ {
		d.observeAt("inst-A", "running", base.Add(time.Duration(i)*5*time.Second))
	}
	d.observeAt("inst-B", "running", base.Add(2*time.Second))

	logging.Shutdown()
	body := readLogFile(t, logDir)
	if !strings.Contains(body, `"session":"inst-A"`) {
		t.Errorf("expected inst-A flicker; got:\n%s", body)
	}
	if strings.Contains(body, `"session":"inst-B"`) {
		t.Errorf("did not expect inst-B in flicker log; got:\n%s", body)
	}
}

// TestFlickerDetector_RateLimited verifies that a sustained flicker burst
// produces only ONE flicker_detected log within the cooldown window
// (otherwise a hot-looping session would spam the log).
func TestFlickerDetector_RateLimited(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug: true, LogDir: logDir, Level: "debug", Format: "json",
	})
	defer logging.Shutdown()

	d := NewFlickerDetector()
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 12; i++ {
		d.observeAt("inst-A", "running", base.Add(time.Duration(i)*2*time.Second))
	}

	logging.Shutdown()
	body := readLogFile(t, logDir)
	count := strings.Count(body, `"flicker_detected"`)
	if count != 1 {
		t.Errorf("expected exactly 1 flicker_detected log within cooldown, got %d:\n%s", count, body)
	}
}

func readLogFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if err != nil {
		if os.IsNotExist(err) {
			// Lumberjack only creates the file on first write. No file = no log = OK.
			return ""
		}
		t.Fatalf("read debug.log: %v", err)
	}
	return string(data)
}
