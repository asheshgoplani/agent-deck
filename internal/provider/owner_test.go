package provider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// configureFakeOwner writes [providers.fake] plus [sandbox] owner.
func configureFakeOwner(t *testing.T, owner string) {
	t.Helper()
	cfg := &session.UserConfig{
		Providers: map[string]session.ProviderConfig{
			"fake": {Command: fakeProviderPath(t), TimeoutSeconds: 10, CreateTimeoutSeconds: 10},
		},
		Sandbox: session.SandboxSettings{Owner: owner},
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// ASK-172: the create request names the owner (agent-deck/owner label) and
// the registered remote keeps it, so every view can show whose sandbox it is.
func TestProvisionSendsAndStoresOwner(t *testing.T) {
	tests := []struct {
		name, configured, user, want string
	}{
		{name: "configured owner", configured: "alice", user: "dev", want: "alice"},
		{name: "login name when none is configured", configured: "", user: "bob", want: "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			freshHome(t)
			t.Setenv("USER", tt.user)
			configureFakeOwner(t, tt.configured)
			state := fakeEnv(t, "ok")
			if _, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "own1", WaitSSH: okSeam, Install: okSeam}); err != nil {
				t.Fatalf("Provision: %v", err)
			}
			reqs := strings.Join(readLines(t, filepath.Join(state, "requests.log")), "\n")
			if !strings.Contains(reqs, `"agent-deck/owner":"`+tt.want+`"`) {
				t.Fatalf("create request lacks owner label %q: %s", tt.want, reqs)
			}
			if rc := loadRemotes(t)["own1"]; rc.Owner != tt.want {
				t.Fatalf("registered remote owner = %q, want %q (%+v)", rc.Owner, tt.want, rc)
			}
		})
	}
}

// An owner that cannot be a label (control character, too long) is refused
// before anything is created.
func TestProvisionRefusesAnInvalidOwner(t *testing.T) {
	for _, owner := range []string{"al\x1bice", strings.Repeat("a", 65)} {
		freshHome(t)
		configureFakeOwner(t, owner)
		state := fakeEnv(t, "ok")
		_, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "own2", WaitSSH: okSeam, Install: okSeam})
		if err == nil || !strings.Contains(err.Error(), "[sandbox] owner") {
			t.Fatalf("owner %q: err = %v, want a refusal naming [sandbox] owner", owner, err)
		}
		if created := readLines(t, filepath.Join(state, "created.log")); len(created) != 0 {
			t.Fatalf("owner %q: a sandbox was created anyway: %v", owner, created)
		}
	}
}

// Review of #3019: an owner with a control character is refused whatever
// its source, and the refusal names where the value came from ($USER is
// not a setting the user made) without echoing it.
func TestProvisionRefusesAControlCharOwner_NamesItsSource(t *testing.T) {
	for _, tt := range []struct{ name, configured, user, wantSource, notSource string }{
		{name: "from config", configured: "al\nice", user: "dev", wantSource: "[sandbox] owner", notSource: "$USER"},
		{name: "from $USER", configured: "", user: "bo\x1b[2Jb", wantSource: "$USER", notSource: "[sandbox] owner"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			freshHome(t)
			t.Setenv("USER", tt.user)
			configureFakeOwner(t, tt.configured)
			state := fakeEnv(t, "ok")
			_, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "own3", WaitSSH: okSeam, Install: okSeam})
			if err == nil || !strings.Contains(err.Error(), tt.wantSource) || strings.Contains(err.Error(), tt.notSource) {
				t.Fatalf("err = %v, want a refusal naming %s (not %s)", err, tt.wantSource, tt.notSource)
			}
			if strings.ContainsAny(err.Error(), "\x1b\n") {
				t.Fatalf("refusal echoes the control characters: %q", err.Error())
			}
			if created := readLines(t, filepath.Join(state, "created.log")); len(created) != 0 {
				t.Fatalf("a sandbox was created anyway: %v", created)
			}
		})
	}
}
