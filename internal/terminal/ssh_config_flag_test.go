package terminal

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sshcontrol"
)

// A provider sandbox remote's attach (Shift+Enter launcher) must pass the
// sandbox ssh_config with -F, on the ssh path and on both halves of the mosh
// path (probe and mosh's own --ssh).
func TestBuildAttachCommand_SSHConfigFlag(t *testing.T) {
	const cfg = "/data/sandboxes/docker-a/ssh_config"
	ssh := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "agentdeck-sb-docker-a", SSHConfig: cfg}})
	if !strings.HasPrefix(ssh, "ssh -tt -F '"+cfg+"' ") {
		t.Fatalf("ssh attach lacks -F: %s", ssh)
	}
	mosh := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "agentdeck-sb-docker-a", SSHConfig: cfg, Transport: "mosh"}})
	if n := strings.Count(mosh, "-F "); n < 3 {
		t.Fatalf("mosh attach should pass -F to the probe, mosh --ssh and the ssh fallback (%d found): %s", n, mosh)
	}
	plain := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "me@box", Transport: "mosh"}})
	if strings.Contains(plain, "-F ") {
		t.Fatalf("plain remote got -F: %s", plain)
	}
	args := MoshArgsWithSSHConfig("h", "", cfg, []string{"x"})
	if !strings.Contains(args[1], "ssh -F "+cfg+" ") {
		t.Fatalf("MoshArgsWithSSHConfig --ssh lacks -F: %q", args[1])
	}
}

// The sandbox attach (ssh, mosh probe, mosh --ssh) must use the sandbox's own
// ControlPath, never the shared %C one a plain remote with the same resolved
// host/port/user could have a live master on.
func TestBuildAttachCommand_SSHConfigUsesOwnControlPath(t *testing.T) {
	const cfg = "/data/sandboxes/docker-a/ssh_config"
	own := sshcontrol.ControlPathFor(cfg)
	shared := sshcontrol.ControlPath()
	if own == shared {
		t.Fatalf("ControlPathFor(%q) equals the shared ControlPath", cfg)
	}
	ssh := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "agentdeck-sb-docker-a", SSHConfig: cfg}})
	if !strings.Contains(ssh, "ControlPath="+shellQuote(own)) || strings.Contains(ssh, shared) {
		t.Fatalf("ssh attach should use %q: %s", own, ssh)
	}
	mosh := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "agentdeck-sb-docker-a", SSHConfig: cfg, Transport: "mosh"}})
	if strings.Contains(mosh, shared) {
		t.Fatalf("mosh attach still uses the shared ControlPath %q: %s", shared, mosh)
	}
	args := MoshArgsWithSSHConfig("h", "", cfg, []string{"x"})
	if !strings.Contains(args[1], "ControlPath="+own+" ") {
		t.Fatalf("mosh --ssh should use %q: %q", own, args[1])
	}
	plain := BuildAttachCommand(AttachRequest{Name: "s1", Remote: &RemoteAttach{Host: "me@box"}})
	if !strings.Contains(plain, shellQuote(shared)) {
		t.Fatalf("plain remote lost the shared ControlPath: %s", plain)
	}
}
