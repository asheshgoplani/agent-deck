package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRemotePollStatePersistence(t *testing.T) {
	setupSessionXDGPathEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rc := RemoteConfig{Host: "lab.example"}
	if len(LoadRemotePolls()) != 0 {
		t.Fatal("fresh cache must be unknown")
	}
	state := RecordRemotePoll("lab", rc, 42*time.Millisecond, errors.New("Permission denied (publickey): secret-token\x1b[31m"))
	if state.LastPollStatus != "auth_failed" || *state.LastPollMS != 42 {
		t.Fatalf("state: %+v", state)
	}
	if strings.Contains(state.LastPollError, "secret") || strings.Contains(state.LastPollError, "\x1b") {
		t.Fatal("unsafe reason")
	}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"other": {Version: "1.0.0", Found: true}}); err != nil {
		t.Fatal(err)
	}
	cached := LoadRemotePolls()["lab"]
	if cached.LastPollStatus != "auth_failed" || !cached.Matches(rc) {
		t.Fatalf("cached auth lost: %+v", cached)
	}
	for _, changed := range []RemoteConfig{{Host: "other"}, {Host: rc.Host, Profile: "other"}, {Host: rc.Host, AgentDeckPath: "/other"}} {
		if cached.Matches(changed) {
			t.Fatalf("identity matched %+v", changed)
		}
	}
	if err := ResetRemotePoll("lab"); err != nil {
		t.Fatal(err)
	}
	if len(LoadRemotePolls()) != 0 {
		t.Fatal("retry did not clear block")
	}
	if LoadRemoteVersions()["other"].Version != "1.0.0" {
		t.Fatal("reset lost versions")
	}
}

func TestRemotePollErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status string
	}{
		{errors.New("open /state: permission denied"), "error"},
		{fmt.Errorf("tailscale SSH requires additional check: %w", context.DeadlineExceeded), "auth_failed"},
		{nil, "ok"}, {context.Canceled, "unknown"}, {fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "timeout"},
		{errors.New("tailscale SSH requires an additional check"), "auth_failed"},
		{errors.New("Permission denied (publickey)"), "auth_failed"},
		{errors.New("ssh: connect: Connection timed out"), "timeout"},
		{errors.New("ssh: connect: No route to host"), "host_down"},
		{errors.New("ssh: Could not resolve hostname lab"), "host_down"},
		{errors.New("remote JSON invalid: secret-token"), "error"},
	} {
		status, reason := remotePollError(tc.err)
		if status != tc.status {
			t.Errorf("%v: got %s want %s", tc.err, status, tc.status)
		}
		if strings.Contains(reason, "secret-token") {
			t.Fatal("unsafe reason")
		}
	}
}
