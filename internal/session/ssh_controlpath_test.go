package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sshcontrol"
)

// Issue #2837: the session package must emit sshcontrol.ControlPath(), whose
// length is tested in internal/terminal/ssh_controlpath_test.go and
// internal/sshcontrol/controlpath_len_test.go.

func controlPathValues(t *testing.T, label, emitted string) []string {
	t.Helper()
	var out []string
	for _, f := range strings.Fields(emitted) {
		if v, ok := strings.CutPrefix(f, "ControlPath="); ok {
			out = append(out, v)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%s: found ControlPath values %v, want exactly 1 in %q", label, out, emitted)
	}
	return out
}

func TestIssue2837_SessionSSHConnOptsControlPathFits(t *testing.T) {
	got := controlPathValues(t, "sessionSSHConnOpts", strings.Join(sessionSSHConnOpts(), " "))[0]
	if want := sshcontrol.ControlPath(); got != want {
		t.Errorf("sessionSSHConnOpts ControlPath = %q, want %q", got, want)
	}
}

func TestIssue2837_WrapForSSHControlPathFits(t *testing.T) {
	inst := NewInstance("ctl-path", "/tmp/proj")
	inst.SSHHost = "alice@remote"
	inst.SSHRemotePath = "/srv/app"
	got := controlPathValues(t, "wrapForSSH", inst.wrapForSSH("echo hi"))[0]
	// wrapForSSH builds a shell command line, so the path is shell-quoted.
	if want := shellQuote(sshcontrol.ControlPath()); got != want {
		t.Errorf("wrapForSSH ControlPath = %q, want %q", got, want)
	}
}
