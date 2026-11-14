package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sshcontrol"
)

// argsHaveSSHConfig reports whether args pass "-F <path>" before host.
func argsHaveSSHConfig(args []string, path, host string) bool {
	hostAt := -1
	for i, a := range args {
		if a == host {
			hostAt = i
			break
		}
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-F" && args[i+1] == path {
			return hostAt < 0 || i < hostAt
		}
	}
	return false
}

// A provider sandbox remote carries its own ssh_config; every ssh argv the
// runner builds (one-shot exec, dedicated retry, channel, attach) must pass
// it with -F, and a plain remote must not get -F at all.
func TestSSHRunner_SSHConfigPassedToEverySSHArgv(t *testing.T) {
	const cfg = "/data/agent-deck/sandboxes/docker-sb1/ssh_config"
	const host = "agentdeck-sb-docker-sb1"
	r := NewSSHRunner("docker-sb1", RemoteConfig{Host: host, SSHConfig: cfg, Provider: "docker", SandboxID: "sb1"})

	cases := map[string][]string{
		"base":      r.sshBaseArgs("true"),
		"dedicated": r.dedicatedSSHArgs("true"),
		"channel":   r.sshChannelArgs("true"),
		"attach":    r.sshAttachArgs("session", "attach", "x"),
		"connOpts":  append(r.sshConnOpts(), host),
	}
	for name, args := range cases {
		if !argsHaveSSHConfig(args, cfg, host) {
			t.Errorf("%s argv lacks -F %s before the host: %q", name, cfg, args)
		}
	}
	mosh := r.moshAttachArgs("session", "attach", "x")
	found := false
	for _, a := range mosh {
		if strings.HasPrefix(a, "--ssh=") && strings.Contains(a, "-F "+cfg) {
			found = true
		}
	}
	if !found {
		t.Errorf("mosh --ssh lacks -F %s: %q", cfg, mosh)
	}

	plain := NewSSHRunner("plain", RemoteConfig{Host: "me@box"})
	for _, a := range plain.sshBaseArgs("true") {
		if a == "-F" {
			t.Fatalf("plain remote got -F: %q", plain.sshBaseArgs("true"))
		}
	}
	for _, a := range plain.moshAttachArgs("x") {
		if strings.Contains(a, " -F ") {
			t.Fatalf("plain remote mosh got -F: %q", a)
		}
	}
}

// A sandbox remote gets its own mux namespace: the shared %C ControlPath
// hashes only host, port and user, so a sandbox resolving to the same
// endpoint as a plain remote would reuse that remote's live master and skip
// its own ssh_config checks (pinned host keys, known_hosts, IdentityFile).
func TestSSHRunner_SSHConfigUsesOwnControlPath(t *testing.T) {
	const cfg = "/x/ssh_config"
	r := NewSSHRunner("a", RemoteConfig{Host: "agentdeck-sb-a", SSHConfig: cfg})
	want := "ControlPath=" + sshcontrol.ControlPathFor(cfg)
	shared := "ControlPath=" + sshcontrol.ControlPath()
	if want == shared {
		t.Fatalf("ControlPathFor(%q) equals the shared ControlPath %q", cfg, shared)
	}
	cases := map[string][]string{
		"base":     r.sshBaseArgs("true"),
		"channel":  r.sshChannelArgs("true"),
		"attach":   r.sshAttachArgs("session", "attach", "x"),
		"connOpts": r.sshConnOpts(),
	}
	for name, args := range cases {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, want) || strings.Contains(joined, shared) {
			t.Errorf("%s argv should use the sandbox ControlPath %q, got: %s", name, want, joined)
		}
	}
	mosh := strings.Join(r.moshAttachArgs("session", "attach", "x"), " ")
	if !strings.Contains(mosh, want) || strings.Contains(mosh, shared) {
		t.Errorf("mosh --ssh should use the sandbox ControlPath %q, got: %s", want, mosh)
	}
	plain := NewSSHRunner("p", RemoteConfig{Host: "me@box"})
	if joined := strings.Join(plain.sshBaseArgs("true"), " "); !strings.Contains(joined, shared) {
		t.Errorf("plain remote lost the shared %%C ControlPath: %s", joined)
	}
}
