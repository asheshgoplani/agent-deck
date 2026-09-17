package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteListPollUnknown(t *testing.T) {
	setupTask6XDGEnv(t)
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"lab": {Host: "invalid.example"}}}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: %s", out)
	}
	for _, key := range []string{"last_poll_ms", "last_poll_status", "last_poll_error"} {
		if _, ok := rows[0][key]; !ok {
			t.Errorf("missing %s: %s", key, out)
		}
	}
	if rows[0]["last_poll_status"] != "unknown" || rows[0]["last_poll_ms"] != nil || rows[0]["last_poll_error"] != "" {
		t.Fatalf("unknown poll: %s", out)
	}
}

func TestRemoteListPollCachedAndRetry(t *testing.T) {
	setupTask6XDGEnv(t)
	rc := session.RemoteConfig{Host: "invalid.example"}
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"lab": rc}}); err != nil {
		t.Fatal(err)
	}
	session.RecordRemotePoll("lab", rc, 17*time.Millisecond, errors.New("Permission denied (publickey) token-secret"))
	out := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
	if !strings.Contains(out, `"last_poll_status": "auth_failed"`) || !strings.Contains(out, `"last_poll_ms": 17`) || strings.Contains(out, "token-secret") {
		t.Fatalf("cached output: %s", out)
	}
	human := captureStdout(t, func() { handleRemoteList(nil) })
	if !strings.Contains(human, "auth failed") {
		t.Fatalf("no visible reason: %s", human)
	}
	changed := rc
	changed.Profile = "other"
	if state := configuredRemotePoll(session.LoadRemotePolls()["lab"], changed); state.LastPollStatus != "unknown" || state.LastPollMS != nil {
		t.Fatalf("stale identity: %+v", state)
	}
	out = captureStdout(t, func() { handleRemoteList([]string{"--retry", "--json"}) })
	if !strings.Contains(out, `"last_poll_status": "unknown"`) || len(session.LoadRemotePolls()) != 0 {
		t.Fatalf("retry failed: %s", out)
	}
}

func TestRemoteAutoUpdateRespectsPollAuthBlock(t *testing.T) {
	setupTask6XDGEnv(t)
	rc := session.RemoteConfig{Host: "lab.example"}
	session.RecordRemotePoll("blocked", rc, time.Millisecond, errors.New("Permission denied (publickey)"))
	session.RecordRemotePoll("changed", session.RemoteConfig{Host: "old.example"}, time.Millisecond, errors.New("Permission denied (publickey)"))
	old := remoteAutoUpdateRunner
	t.Cleanup(func() { remoteAutoUpdateRunner = old })
	calls := map[string]int{}
	remoteAutoUpdateRunner = func(name string, _ session.RemoteConfig) session.RemoteBinaryInstaller {
		calls[name]++
		return &autoUpdateStub{version: "1.16.0", found: true}
	}
	results := runRemoteAutoUpdate(map[string]session.RemoteConfig{"blocked": rc, "changed": rc}, "1.16.0")
	if calls["blocked"] != 0 || calls["changed"] != 1 {
		t.Fatalf("auth block bypass: %v", calls)
	}
	if len(results) != 2 || results[0].Outcome != session.RemoteUpdateOutcomeSkipped {
		t.Fatalf("results: %+v", results)
	}
}

func TestRemotePollRetryForwarding(t *testing.T) {
	args := []string{"remote", "list", "--retry", "--json"}
	got, err := remoteCommandArgs(args)
	if err != nil || strings.Join(got, " ") != strings.Join(args, " ") {
		t.Fatalf("forward: %v, %v", got, err)
	}
	if _, err := remoteCommandArgs([]string{"remote", "remove", "lab"}); err == nil {
		t.Fatal("unrelated management command accepted")
	}
}
