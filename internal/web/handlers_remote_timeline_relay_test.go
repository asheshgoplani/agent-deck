package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// relayRemote wires the remote timeline route to the production runner
// (configuredRemoteTimelineRunner -> session.SSHRunner.RunReadIO, with its
// real command builder). Only the ssh binary is a stub: like sshd it runs the
// remote command line with sh, and the remote "agent-deck" is a script that
// records what it was started with. It returns the files the stubs write:
// the ssh command line and the AGENTDECK_FROM_REMOTE value the remote saw.
func relayRemote(t *testing.T, srv *Server, core string) (sshLog, markLog string) {
	t.Helper()
	dir := t.TempDir()
	sshLog = filepath.Join(dir, "ssh-command")
	markLog = filepath.Join(dir, "remote-mark")
	shim := "#!/bin/sh\nfor arg do command=$arg; done\nprintf '%s\\n' \"$command\" >> '" + sshLog + "'\nexec sh -c \"$command\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// This process must not hand the mark down by itself.
	t.Setenv(session.RemoteServedEnv, "")
	answer := filepath.Join(dir, "answer.json")
	if err := os.WriteFile(answer, []byte(remoteTimelineAnswer(t, "remote-1")), 0o600); err != nil {
		t.Fatal(err)
	}
	corePath := filepath.Join(dir, "agent-deck")
	script := "#!/bin/sh\nfor arg do [ \"$arg\" = --help ] && exit 0; done\n" +
		"printf '%s' \"${" + session.RemoteServedEnv + ":-unset}\" > '" + markLog + "'\n" +
		"ANSWER='" + answer + "'\n" + core
	if err := os.WriteFile(corePath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[recall]\nenabled = true\n\n[remotes.mac]\nhost = \"mac\"\nagent_deck_path = %q\n", corePath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	srv.remoteTimelineRunner = nil
	return sshLog, markLog
}

// The web remote route's ssh command carries the relay mark end to end: built
// by the real SSHRunner, it reaches the remote process as
// AGENTDECK_FROM_REMOTE=1, so a recall-off remote refuses the read and the
// route answers 503 RECALL_DISABLED.
func TestRemoteSessionTimeline_RelayMarkReachesTheRemote(t *testing.T) {
	for _, tc := range []struct {
		name   string
		core   string
		status int
		code   string
	}{
		// A remote with recall on serves the rows.
		{"recall on", "cat \"$ANSWER\"\n", http.StatusOK, ""},
		// A remote with recall off, as this build decides it: the relay mark
		// refuses with "recall is off" (exit 2); an unmarked read is served.
		{"recall off", "if [ \"$" + session.RemoteServedEnv + "\" = 1 ]; then\n printf '%s\\n' '{\"success\":false,\"error\":\"recall is off: set [recall] enabled = true in config.toml\",\"code\":\"INVALID_OPERATION\"}'\n exit 2\nfi\ncat \"$ANSWER\"\n", http.StatusServiceUnavailable, "RECALL_DISABLED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRemoteTimelineServer(t, &fakeRemoteRunner{}, true)
			sshLog, markLog := relayRemote(t, srv, tc.core)
			rr := getRemoteTimeline(srv, "/api/remotes/mac/sessions/remote-1/timeline?tail=60")
			mark, _ := os.ReadFile(markLog)
			command, _ := os.ReadFile(sshLog)
			if string(mark) != "1" {
				t.Fatalf("remote saw %s=%q, want \"1\"; ssh command: %s", session.RemoteServedEnv, mark, command)
			}
			if !strings.Contains(string(command), "env "+session.RemoteServedEnv+"=1 ") {
				t.Fatalf("ssh command has no relay mark: %s", command)
			}
			if tc.code != "" {
				wantAPIError(t, rr, tc.status, tc.code)
				return
			}
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), "remote answer") {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}
