package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
)

// TestOptiming_UpdateCheckAndInstallRecorded: with timings on, a release
// check records update.check and an install attempt records update.install,
// failed when it fails (ASK-106).
func TestOptiming_UpdateCheckAndInstallRecorded(t *testing.T) {
	isolateUpdatePaths(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Release{TagName: "v9.9.9"})
	}))
	defer srv.Close()
	origURL := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = origURL })
	origDetect := detectHomebrewManagedInstall
	detectHomebrewManagedInstall = func() (string, string, bool, error) {
		return "/opt/homebrew/bin/agent-deck", "brew upgrade agent-deck", true, nil
	}
	t.Cleanup(func() { detectHomebrewManagedInstall = origDetect })

	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, true))

	if _, _, _, err := fetchLatestReleaseConditional(""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if err := PerformVerifiedUpdate(&Release{TagName: "v9.9.9"}, "linux", "amd64"); err == nil {
		t.Fatal("homebrew-managed install should refuse")
	}

	entries, err := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v err=%v, want 2", entries, err)
	}
	if entries[0].Op != "update.check" || entries[0].Fail {
		t.Errorf("entry 0 = %+v, want update.check ok", entries[0])
	}
	if entries[1].Op != "update.install" || !entries[1].Fail {
		t.Errorf("entry 1 = %+v, want update.install failed", entries[1])
	}
}
