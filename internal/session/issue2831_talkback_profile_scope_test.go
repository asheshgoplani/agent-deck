package session

// Issue #2831 (reported by jwr456): the receiving side of a remote drain never
// checked that the records it ingests belong to the receiving conductor's
// profile. With [remotes.<name>] require_same_profile = true:
//   - RunRemoteTalkback refuses a receiving parent of another profile;
//   - scheduled talkback enrolls only conductors of the remote's profile.
// With the key unset both behave as before.
// Follow-up: an unknown receiving profile is refused (fail closed), the
// remote's profile is compared trimmed, and a failure alert skips conductors
// enrolled before strict mode was set.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"
)

// issue2831Config writes remote "r" (profile B) with or without strict mode.
// It goes through the TOML file, as a user would set it.
func issue2831Config(t *testing.T, strict bool) RemoteConfig {
	t.Helper()
	return issue2831ConfigProfile(t, strict, "B")
}

func issue2831ConfigProfile(t *testing.T, strict bool, profile string) RemoteConfig {
	t.Helper()
	body := "[remotes.r]\nhost = \"w@r\"\nprofile = \"" + profile + "\"\ntalkback_interval_secs = 30\n"
	if strict {
		body += "require_same_profile = true\n"
	}
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Remotes["r"]
}

func TestIssue2831_RunRemoteTalkbackRefusesParentOfOtherProfile(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "default-off"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			cursorTestHome(t)
			rc := issue2831Config(t, strict)
			parent := conductorParent("conductor-of-a")
			rec := TransitionNotificationEvent{ChildSessionID: "b-child", ChildTitle: "b worker", Profile: "B",
				FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now().Add(-time.Minute)}

			deps := SSHTalkbackDeps("r", rc)
			deps.FetchAfter = func(context.Context, RemoteCursor) (RemoteExport, error) {
				return RemoteExport{}, ErrRemoteCursorUnsupported
			}
			deps.FetchAll = func(context.Context) ([]TransitionNotificationEvent, error) {
				return []TransitionNotificationEvent{rec}, nil
			}
			deps.WriterProbe = func(context.Context) (WriterStatus, error) { return WriterStatus{Running: true}, nil }
			deps.Parent = func() (*Instance, string) { return parent, "A" }
			deps.Wake = func(*Instance, string, TransitionNotificationEvent) {}

			_, err := RunRemoteTalkback(context.Background(), "r", parent.ID, deps)
			pending, _ := ReadInboxEvents(parent.ID)
			if strict {
				if err == nil || len(pending) != 0 {
					t.Fatalf("strict: profile B records reached profile A's conductor: err=%v pending=%d", err, len(pending))
				}
				return
			}
			if err != nil || len(pending) != 1 {
				t.Fatalf("default-off: drain changed: err=%v pending=%d", err, len(pending))
			}
		})
	}
}

func TestIssue2831_TalkbackEnrollsOnlyRemoteProfileConductors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		strict  bool
		profile string
		want    []string
	}{
		{"strict", true, "B", []string{"B"}},
		{"strict/padded-profile", true, " B ", []string{"B"}},
		{"default-off", false, "B", []string{"A", "B", "C"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cursorTestHome(t)
			issue2831ConfigProfile(t, tc.strict, tc.profile)
			for _, profile := range []string{"A", "B", "C"} {
				storage, err := NewStorageWithProfile(profile)
				if err != nil {
					t.Fatal(err)
				}
				inst := conductorParent("conductor-of-" + profile)
				err = storage.SaveWithGroups([]*Instance{inst}, nil)
				storage.Close()
				if err != nil {
					t.Fatal(err)
				}
				if err := SaveRemoteCursor("r", inst.ID, RemoteCursor{Legacy: true}); err != nil {
					t.Fatal(err)
				}
			}

			targets, err := discoverTalkbackTargets("r")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, target := range targets {
				got = append(got, target.Profile)
			}
			sort.Strings(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("enrolled profiles = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssue2831_StrictRefusesUnknownReceivingProfile(t *testing.T) {
	cursorTestHome(t)
	// The process profile equals the remote's, so a fallback to it would pass.
	t.Setenv("AGENTDECK_PROFILE", "B")
	rc := issue2831Config(t, true)
	const parentID = "conductor-unregistered"
	rec := TransitionNotificationEvent{ChildSessionID: "b-child", ChildTitle: "b worker", Profile: "B",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now().Add(-time.Minute)}
	deps := SSHTalkbackDeps("r", rc)
	deps.FetchAfter = func(context.Context, RemoteCursor) (RemoteExport, error) {
		return RemoteExport{}, ErrRemoteCursorUnsupported
	}
	deps.FetchAll = func(context.Context) ([]TransitionNotificationEvent, error) {
		return []TransitionNotificationEvent{rec}, nil
	}
	deps.WriterProbe = func(context.Context) (WriterStatus, error) { return WriterStatus{Running: true}, nil }
	deps.Parent = func() (*Instance, string) { return nil, "" }

	_, err := RunRemoteTalkback(context.Background(), "r", parentID, deps)
	var te *RemoteTalkbackError
	if !errors.As(err, &te) || te.Stage != RemoteTalkbackStageProfile {
		t.Fatalf("strict drain with an unknown receiving profile was not refused: err=%v", err)
	}
	if pending, _ := ReadInboxEvents(parentID); len(pending) != 0 {
		t.Fatalf("records ingested despite the refusal: %d", len(pending))
	}
}

func TestIssue2831_StrictAlertSkipsTargetsOfOtherProfiles(t *testing.T) {
	cursorTestHome(t)
	a, b := conductorParent("conductor-of-A"), conductorParent("conductor-of-B")
	clock := &talkbackClock{now: time.Now()}
	alerted := map[string]bool{}
	s := &remoteTalkbackScheduler{
		// Both conductors were enrolled before require_same_profile was set.
		state: map[string]*remoteTalkbackState{"r": {targets: []remoteTalkbackTarget{{Parent: a, Profile: "A"}, {Parent: b, Profile: "B"}}}},
		now:   func() time.Time { return clock.now },
		remotes: func() map[string]RemoteConfig {
			return map[string]RemoteConfig{"r": {Host: "w@r", Profile: "B", RequireSameProfile: true, TalkbackIntervalSecs: 30}}
		},
		targets: func(string) ([]remoteTalkbackTarget, error) { return nil, errors.New("discovery failed") },
		drain:   func(context.Context, string, RemoteConfig, remoteTalkbackTarget) error { return nil },
		alert:   func(_ string, t remoteTalkbackTarget, _ string, _ time.Time) { alerted[t.Profile] = true },
	}
	for range remoteTalkbackAlertAfter {
		s.tick(context.Background())
		s.wg.Wait()
		clock.now = clock.now.Add(remoteTalkbackBackoffMax)
	}
	if alerted["A"] || !alerted["B"] {
		t.Fatalf("alerted profiles = %v, want only B", alerted)
	}
}
