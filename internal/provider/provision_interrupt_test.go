package provider

// #2918 items 4 and 5: a create that is cut off (timeout, Ctrl-C, TUI quit)
// after the provider already made the sandbox is cleaned up, and two
// provisionings of the same remote name cannot run at once.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// configureFakeCreateTimeout writes [providers.fake] with the given create
// timeout.
func configureFakeCreateTimeout(t *testing.T, seconds int) {
	t.Helper()
	cfg := &session.UserConfig{Providers: map[string]session.ProviderConfig{
		"fake": {Command: fakeProviderPath(t), TimeoutSeconds: 10, CreateTimeoutSeconds: seconds},
	}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionSweepsSandboxLeftByInterruptedCreate(t *testing.T) {
	for _, tc := range []string{"timeout", "cancel"} {
		t.Run(tc, func(t *testing.T) {
			freshHome(t)
			configureFakeCreateTimeout(t, map[string]int{"timeout": 1, "cancel": 30}[tc])
			state := fakeEnv(t, "createhang")
			// A sandbox left by an earlier, forgotten remote of the same name:
			// not made by this create, so the sweep must leave it alone.
			if err := os.WriteFile(filepath.Join(state, "created.log"), []byte("sb-old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "labels.log"),
				[]byte(`sb-old {"agent-deck/create-id":"0123456789abcdef","agent-deck/remote":"box1"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				time.AfterFunc(1500*time.Millisecond, cancel)
				defer cancel()
			}
			_, err := Provision(ctx, ProvisionOptions{Provider: "fake", RemoteName: "box1", WaitSSH: okSeam, Install: okSeam})
			if err == nil {
				t.Fatal("Provision succeeded although create never answered")
			}
			if tc == "timeout" && !errors.Is(err, ErrTimeout) {
				t.Errorf("timeout error is not typed (errors.Is ErrTimeout): %v", err)
			}
			if tc == "cancel" && !errors.Is(err, context.Canceled) {
				t.Errorf("cancel error does not wrap context.Canceled: %v", err)
			}
			created := readLines(t, filepath.Join(state, "created.log"))
			destroyed := readLines(t, filepath.Join(state, "destroyed.log"))
			if len(created) != 2 {
				t.Fatalf("created = %q, want sb-old plus the interrupted create", created)
			}
			if len(destroyed) != 1 || destroyed[0] != created[1] {
				t.Fatalf("destroyed = %q, want only the interrupted create %q", destroyed, created[1])
			}
			if !strings.Contains(err.Error(), created[1]) {
				t.Errorf("error does not name the swept sandbox %s: %v", created[1], err)
			}
			if _, ok := loadRemotes(t)["box1"]; ok {
				t.Fatal("remote registered after an interrupted create")
			}
		})
	}
}

// When the sweep cannot list the provider's sandboxes, the error says to
// check by hand, with the label to look for.
func TestProvisionSweepFailureIsReported(t *testing.T) {
	freshHome(t)
	configureFakeCreateTimeout(t, 1)
	fakeEnv(t, "createhang")
	t.Setenv("FAKE_PROVIDER_LIST", "fail")
	_, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "box2", WaitSSH: okSeam, Install: okSeam})
	if err == nil || !strings.Contains(err.Error(), "could not check for a leftover sandbox") || !strings.Contains(err.Error(), "agent-deck/remote=box2") {
		t.Fatalf("err = %v, want it to say the leftover check failed and name the label", err)
	}
}

func TestProvisionRefusesConcurrentSameName(t *testing.T) {
	freshHome(t)
	configureFake(t)
	state := fakeEnv(t, "ok")
	unlock, err := lockRemoteName("box3")
	if err != nil {
		t.Fatalf("lockRemoteName: %v", err)
	}
	_, err = Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "box3", WaitSSH: okSeam, Install: okSeam})
	if err == nil || !strings.Contains(err.Error(), "already being provisioned") {
		t.Fatalf("err = %v, want a refusal while another provisioning holds the name", err)
	}
	if n := len(readLines(t, filepath.Join(state, "created.log"))); n != 0 {
		t.Fatalf("%d sandbox(es) created while the name was locked", n)
	}
	unlock()
	if _, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "box3", WaitSSH: okSeam, Install: okSeam}); err != nil {
		t.Fatalf("Provision after unlock: %v", err)
	}
	// Provision released its own lock.
	unlock2, err := lockRemoteName("box3")
	if err != nil {
		t.Fatalf("name still locked after Provision returned: %v", err)
	}
	unlock2()
}
