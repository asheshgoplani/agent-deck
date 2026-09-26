package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMoshArgs(t *testing.T) {
	got := MoshArgs("me@box", " /opt/homebrew/bin/mosh-server ", []string{"/bin/agent deck", "-p", "w", "session", "attach", "id"})
	want := []string{
		"--experimental-remote-ip=remote",
		"--ssh=ssh -o ControlMaster=auto -o ControlPath=/tmp/agent-deck-ssh/%r@%h:%p -o ControlPersist=600 -o ConnectTimeout=10 -o BatchMode=yes",
		"--server=/opt/homebrew/bin/mosh-server",
		"me@box", "--",
		"/bin/agent deck", "-p", "w", "session", "attach", "id",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MoshArgs = %q\nwant       %q", got, want)
	}
	if got := MoshArgs("h", "", []string{"x"}); got[2] != "h" {
		t.Fatalf("empty mosh_server still passed --server: %q", got)
	}
}

func TestMoshServerProbe(t *testing.T) {
	for server, want := range map[string]string{
		"":                                  "command -v 'mosh-server'",
		" /opt/homebrew/bin/mosh-server ":   "command -v '/opt/homebrew/bin/mosh-server'",
		"/usr/local/bin/mosh-server new -v": "command -v '/usr/local/bin/mosh-server'",
	} {
		if got := MoshServerProbe(server); got != want {
			t.Errorf("MoshServerProbe(%q) = %q, want %q", server, got, want)
		}
	}
}

func TestUsesMosh(t *testing.T) {
	var nilRemote *RemoteAttach
	for _, tc := range []struct {
		r    *RemoteAttach
		want bool
	}{{nilRemote, false}, {&RemoteAttach{}, false}, {&RemoteAttach{Transport: "ssh"}, false}, {&RemoteAttach{Transport: " MOSH "}, true}} {
		if got := tc.r.UsesMosh(); got != tc.want {
			t.Errorf("UsesMosh(%+v) = %v, want %v", tc.r, got, tc.want)
		}
	}
}

// The mosh command must reach mosh as exactly MoshArgs' argv once a shell
// has parsed it, whatever quotes the operands carry.
func TestBuildAttachCommand_RemoteMoshArgv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mosh"), []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The fake ssh answers the mosh-server probe from $PROBE and otherwise
	// prints its argv, so the ssh fallback is observable too.
	fakeSSH := "#!/bin/sh\ncase \"$*\" in *'command -v'*) exit $PROBE;; esac\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := &RemoteAttach{Host: "me@box", AgentDeckPath: "/bin/agent ' deck", Profile: "wo'rk", Transport: "mosh", MoshServer: "/opt/mosh-server"}
	command := BuildAttachCommand(AttachRequest{Name: "id ' 1", Remote: remote})
	if !strings.Contains(command, "exec mosh ") {
		t.Fatalf("mosh remote rendered %q", command)
	}
	run := func(probe string) []string {
		cmd := exec.Command("/bin/sh", "-c", "exec "+command)
		cmd.Env = append(os.Environ(), "PATH="+dir, "PROBE="+probe)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}
	want := MoshArgs("me@box", "/opt/mosh-server", []string{"/bin/agent ' deck", "-p", "wo'rk", "session", "attach", "id ' 1"})
	if got := run("0"); !reflect.DeepEqual(got, want) {
		t.Fatalf("mosh saw %q\nwant     %q", got, want)
	}
	// No mosh-server on the remote: the same command attaches over ssh.
	wantSSH := []string{"-tt", "-o", "ControlMaster=auto", "-o", "ControlPath=" + SSHControlDir + "/%r@%h:%p", "-o", "ControlPersist=600", "me@box", "/bin/agent ' deck", "-p", "wo'rk", "session", "attach", "id ' 1"}
	if got := run("1"); !reflect.DeepEqual(got, wantSSH) {
		t.Fatalf("fallback ssh saw %q\nwant              %q", got, wantSSH)
	}

	remote.Transport = "ssh"
	if got := BuildAttachCommand(AttachRequest{Name: "id", Remote: remote}); !strings.HasPrefix(got, "ssh -tt") {
		t.Fatalf("ssh transport rendered %q", got)
	}
}
