package main

// Issue #2831 (reported by jwr456): `remote drain --into <id>` resolved a full
// id across every profile, so `-p b remote drain r --into <a's conductor>`
// ingested profile b's records into profile a's inbox. With
// [remotes.r] require_same_profile = true, --into resolves within the
// effective profile only; unset, the drain behaves as before.
// Follow-up: strict --into keeps the cross-profile duplicate-id refusal, and a
// receiving conductor of another profile exits 1 at the profile stage.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue2831_RemoteDrainIntoResolvesWithinEffectiveProfile(t *testing.T) {
	for _, tc := range []struct {
		name       string
		strict     bool
		effective  string   // -p / AGENTDECK_PROFILE
		into       []string // profiles holding the --into conductor id
		wantCode   int
		wantRecs   int
		wantStderr string
	}{
		{name: "strict/other-profile", strict: true, effective: "b", into: []string{"a"}, wantCode: 2},
		{name: "strict/same-profile", strict: true, effective: "b", into: []string{"b"}, wantRecs: 1},
		{name: "strict/duplicate-id", strict: true, effective: "b", into: []string{"a", "b"}, wantCode: 3, wantStderr: "exists in profiles"},
		{name: "strict/profile-stage", strict: true, effective: "a", into: []string{"a"}, wantCode: 1, wantStderr: "requires the same profile"},
		{name: "default-off/other-profile", strict: false, effective: "b", into: []string{"a"}, wantRecs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drainTestHome(t)
			t.Setenv("AGENTDECK_PROFILE", tc.effective)
			body := "[remotes.r]\nhost = \"w@r\"\nprofile = \"b\"\n"
			if tc.strict {
				body += "require_same_profile = true\n"
			}
			path, err := session.GetUserConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()

			conductor := "c0ffee00-1777002831"
			for _, profile := range []string{"a", "b"} {
				inst := session.NewInstance("conductor-"+profile, t.TempDir())
				if slices.Contains(tc.into, profile) {
					inst.ID = conductor
				}
				saveInboxResolutionSessions(t, profile, inst)
			}

			rec := remoteCompletion("b-child", "b private summary", time.Now().Add(-time.Minute))
			rec.Profile = "b"
			fetch, _ := stubFetch([]session.TransitionNotificationEvent{rec}, nil)
			var stdout, stderr bytes.Buffer
			code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "r"}, fetch)
			pending, _ := session.ReadInboxEvents(conductor)
			if code != tc.wantCode || len(pending) != tc.wantRecs || !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("exit=%d (want %d) pending=%d (want %d) stderr=%s",
					code, tc.wantCode, len(pending), tc.wantRecs, stderr.String())
			}
		})
	}
}
