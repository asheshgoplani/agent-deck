package session

// Regression tests for #1206 (security): the SSH transport had no documented
// host-key stance and Attach() omitted the BatchMode/ConnectTimeout options the
// other paths carried — an unknown/changed host key could hang on an
// interactive prompt instead of failing fast. These tests pin:
//   - the shared connection options never weaken host-key checking
//     (no StrictHostKeyChecking=no, no /dev/null known_hosts),
//   - BatchMode + ConnectTimeout are consistent across run AND attach,
//   - a malicious ssh host (ssh option-injection via leading "-") is rejected.

import (
	"context"
	"strings"
	"testing"
)

func TestSSHConnOpts_SafeHostKeyStance(t *testing.T) {
	r := &SSHRunner{Host: "user@host", AgentDeckPath: "agent-deck"}
	opts := strings.Join(r.sshConnOpts(), " ")

	// MITM holes that must NEVER be emitted.
	if strings.Contains(opts, "StrictHostKeyChecking=no") {
		t.Fatalf("sshConnOpts must never disable host-key checking: %s", opts)
	}
	if strings.Contains(opts, "/dev/null") {
		t.Fatalf("sshConnOpts must never point known_hosts at /dev/null: %s", opts)
	}
	// Fail-fast, non-interactive stance must be present.
	if !strings.Contains(opts, "BatchMode=yes") {
		t.Fatalf("sshConnOpts must set BatchMode=yes for a clear non-interactive failure: %s", opts)
	}
	if !strings.Contains(opts, "ConnectTimeout=") {
		t.Fatalf("sshConnOpts must bound the dial with ConnectTimeout: %s", opts)
	}
}

func TestAttachArgs_ConsistentWithRunPath(t *testing.T) {
	r := &SSHRunner{Host: "user@host", AgentDeckPath: "agent-deck"}
	args := r.buildAttachArgs("sess123")
	joined := strings.Join(args, " ")

	// #1206 regression: Attach() used to omit BatchMode + ConnectTimeout.
	if !strings.Contains(joined, "-tt") {
		t.Fatalf("attach args must force a remote PTY with -tt: %s", joined)
	}
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Fatalf("attach must use BatchMode=yes for parity with run/stream paths: %s", joined)
	}
	if !strings.Contains(joined, "ConnectTimeout=") {
		t.Fatalf("attach must bound the dial with ConnectTimeout: %s", joined)
	}
	if strings.Contains(joined, "StrictHostKeyChecking=no") || strings.Contains(joined, "/dev/null") {
		t.Fatalf("attach must not weaken host-key checking: %s", joined)
	}
	// Still actually attaches to the requested session.
	if !strings.Contains(joined, "session") || !strings.Contains(joined, "attach") || !strings.Contains(joined, "sess123") {
		t.Fatalf("attach args must run `session attach sess123`: %s", joined)
	}
}

func TestSSHBaseArgs_SafeHostKeyStance(t *testing.T) {
	r := &SSHRunner{Host: "user@host", AgentDeckPath: "agent-deck"}
	joined := strings.Join(r.sshBaseArgs("uname -s -m"), " ")
	if strings.Contains(joined, "StrictHostKeyChecking=no") || strings.Contains(joined, "/dev/null") {
		t.Fatalf("sshBaseArgs must not weaken host-key checking: %s", joined)
	}
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Fatalf("sshBaseArgs must set BatchMode=yes: %s", joined)
	}
}

func TestValidateSSHHost(t *testing.T) {
	good := []string{"user@host", "host", "deploy@10.0.0.1", "u@host.example.com"}
	for _, h := range good {
		if err := ValidateSSHHost(h); err != nil {
			t.Errorf("ValidateSSHHost(%q) = %v, want nil", h, err)
		}
	}
	bad := []string{
		"",                    // empty
		"-oProxyCommand=evil", // ssh option-injection
		"-l root host",        // leading dash + whitespace
		"host with space",     // whitespace
		"host\nrm -rf",        // newline injection
	}
	for _, h := range bad {
		if err := ValidateSSHHost(h); err == nil {
			t.Errorf("ValidateSSHHost(%q) = nil, want rejection", h)
		}
	}
}

// A malicious host must be rejected by the real SSH exec paths BEFORE any ssh
// subprocess is spawned. Stubs (runFn/remoteExecFn) are intentionally NOT set
// so we hit the validation guard on the real path.
func TestSSHRunner_RejectsOptionInjectionHost(t *testing.T) {
	r := &SSHRunner{Host: "-oProxyCommand=touch /tmp/pwned", AgentDeckPath: "agent-deck"}

	if _, err := r.run(context.Background(), "list", "--json"); err == nil {
		t.Fatal("run() must reject an option-injection host before spawning ssh")
	}
	if _, err := r.remoteExec(context.Background(), "uname -s", nil); err == nil {
		t.Fatal("remoteExec() must reject an option-injection host before spawning ssh")
	}
	if err := r.Attach("sess123"); err == nil {
		t.Fatal("Attach() must reject an option-injection host before spawning ssh")
	}
}

// buildRemoteCommand already shell-quotes every dynamic operand; pin it for a
// malicious path/arg so command-injection into the remote shell stays closed.
func TestBuildRemoteCommand_QuotesMaliciousPathAndArgs(t *testing.T) {
	r := &SSHRunner{AgentDeckPath: "/bin/agent-deck; rm -rf ~", Profile: "default"}
	got := r.buildRemoteCommand("rename", "id", "$(reboot)")
	// The dangerous binary path must be single-quoted as one operand.
	if !strings.Contains(got, "'/bin/agent-deck; rm -rf ~'") {
		t.Fatalf("malicious agent_deck_path must be single-quoted: %s", got)
	}
	// Command substitution in an arg must be quoted, not left live.
	if !strings.Contains(got, "'$(reboot)'") {
		t.Fatalf("malicious arg must be single-quoted: %s", got)
	}
}
