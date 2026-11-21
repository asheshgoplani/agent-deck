package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestSessionForkOpenCode2RecoversLaunchID(t *testing.T) {
	testutil.RequireTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	launcher := filepath.Join(t.TempDir(), "opencode2")
	const stub = `#!/bin/sh
case "$1:$2" in
  api:session.list)
    printf '%s\n' 'unexpected discovery' > "${0%/*}/session-list-called"
    printf '%s\n' '{"data":[]}' ;;
  api:session.fork)
    [ "$4" = sessionID=ses_launch ] || exit 1
    printf '%s\n' '{"data":{"id":"ses_child"}}' ;;
  api:session.update) ;;
  -s:ses_child) exec sleep 300 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(launcher, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := session.NewInstanceWithTool("parent", t.TempDir(), "opencode2")
	parent.Command = launcher
	pane := parent.GetTmuxSession()
	if err := pane.Start("sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pane.Kill() })
	storage, err := session.NewStorageWithProfile("ch_support_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, nil); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", parent.ID, "-t", "child", "--json")
	if code == 0 || !strings.Contains(stdout, "no resumable session") {
		t.Fatalf("fork did not refuse an unbound session (%d): %s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(launcher), "session-list-called")); !os.IsNotExist(err) {
		t.Fatalf("fork without a tmux ID must not query the shared session listing: stat error = %v", err)
	}
	if err := pane.SetEnvironment("OPENCODE_SESSION_ID", "ses_launch"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "session", "fork", parent.ID, "-t", "child", "--json")
	if code != 0 {
		t.Fatalf("fork failed (%d): %s\n%s", code, stdout, stderr)
	}
	t.Cleanup(func() { runAgentDeck(t, home, "session", "stop", "child") })
	stdout, stderr, code = runAgentDeck(t, home, "session", "show", "child", "--json")
	if code != 0 || !strings.Contains(stdout, `"opencode_session_id": "ses_child"`) || !strings.Contains(stdout, `"can_fork": true`) {
		t.Fatalf("child is not a forkable bound session (%d): %s\n%s", code, stdout, stderr)
	}
}

// Remote OpenCode rows keep their launch ID on the owning host. The local
// pre-sync must not adopt this machine's tmux env, so an unbound remote row
// stays unforkable here; the remote's own `session fork` runs there instead.
func TestSessionForkOpenCode2RemoteSkipsLocalLaunchID(t *testing.T) {
	testutil.RequireTmux(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	parent := session.NewInstanceWithTool("remote-parent", t.TempDir(), "opencode2")
	parent.SSHHost = "lab"
	pane := parent.GetTmuxSession()
	if err := pane.Start("sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pane.Kill() })
	if err := pane.SetEnvironment("OPENCODE_SESSION_ID", "ses_local_only"); err != nil {
		t.Fatal(err)
	}
	storage, err := session.NewStorageWithProfile("ch_support_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, nil); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", parent.ID, "-t", "child", "--json")
	if code == 0 || !strings.Contains(stdout, "no resumable session") {
		t.Fatalf("remote fork adopted the local launch ID (%d): %s\n%s", code, stdout, stderr)
	}
	out, _, _ := runAgentDeck(t, home, "list", "--json")
	if strings.Contains(out, `"child"`) {
		t.Fatalf("refused remote fork still created a local child: %s", out)
	}
}
