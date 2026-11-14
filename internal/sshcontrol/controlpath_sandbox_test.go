package sshcontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A provider sandbox remote must not share the %C mux namespace: %C hashes
// only host, port and user, so a sandbox whose resolved endpoint equals a
// plain remote's would ride that remote's live master and skip its own
// ssh_config (pinned host keys, private known_hosts, IdentityFile).
func TestControlPathFor_SandboxGetsOwnNamespace(t *testing.T) {
	tmp := t.TempDir()
	cfgA := filepath.Join(tmp, "a", "ssh_config")
	cfgB := filepath.Join(tmp, "b", "ssh_config")
	for _, p := range []string{cfgA, cfgB} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("Host agentdeck-sb-x\n  HostName 127.0.0.1\n  Port 2222\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shared := ControlPath()
	if got := ControlPathFor(""); got != shared {
		t.Fatalf("plain remote ControlPathFor(\"\") = %q, want the shared %q", got, shared)
	}
	a, b := ControlPathFor(cfgA), ControlPathFor(cfgB)
	if a == shared || b == shared || a == b {
		t.Fatalf("sandbox ControlPaths must be distinct from the shared one and each other: shared=%q a=%q b=%q", shared, a, b)
	}
	if strings.Contains(a, "%C") {
		t.Fatalf("sandbox ControlPath must not use the shared %%C name: %q", a)
	}
	if shared != "none" && filepath.Dir(a) != filepath.Dir(shared) {
		t.Fatalf("sandbox socket %q is not in the verified control dir %q", a, filepath.Dir(shared))
	}
	if again := ControlPathFor(cfgA); again != a {
		t.Fatalf("ControlPathFor not stable: %q then %q", a, again)
	}
	// A reprovisioned sandbox (new endpoint in the same file) must not reuse
	// a master opened to the old endpoint.
	if err := os.WriteFile(cfgA, []byte("Host agentdeck-sb-x\n  HostName 10.0.0.9\n  Port 22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := ControlPathFor(cfgA); changed == a {
		t.Fatalf("ssh_config content changed but ControlPath stayed %q", a)
	}
}

// The sandbox socket name must still fit the macOS sun_path limit in the
// worst-case fallback directory (#2837).
func TestControlPathFor_FitsUnixSocketLimit(t *testing.T) {
	const maxUID = 4294967294
	preferred := (&resolver{parent: "/tmp", uid: maxUID}).preferred()
	fallback := preferred + "-" + "4294967295"
	bound := fallback + sandboxSocketName("/some/very/long/path/to/agent-deck/sandboxes/provider-name-with-many-chars/ssh_config") +
		strings.Repeat("x", sshTempSuffixLen)
	if len(bound) >= sunPathMax {
		t.Fatalf("ssh would bind %q (%d bytes), over the %d-byte limit", bound, len(bound), sunPathMax-1)
	}
}
