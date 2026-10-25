package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestDockerProviderE2E drives the example docker provider for real:
// create -> validate -> sandbox files -> ssh probe -> status/list -> destroy.
// It skips unless the docker binary and daemon and the example provider
// (examples/providers/docker/provider) are all present.
func TestDockerProviderE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("docker e2e skipped in short mode")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers", "docker", "provider"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("example docker provider not present: %v", err)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker binary not found")
	}
	infoCtx, cancelInfo := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelInfo()
	// #nosec G204 -- fixed docker binary and argv.
	if err := exec.CommandContext(infoCtx, docker, "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}

	freshHome(t)
	if err := session.SaveUserConfig(&session.UserConfig{Providers: map[string]session.ProviderConfig{
		"docker": {Command: script},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	opts := ProvisionOptions{Provider: "docker", RemoteName: "e2e-docker", Profile: "_test",
		Progress: func(s string) { t.Log(s) },
		Install:  func(context.Context, string, session.RemoteConfig) error { return nil },
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		opts.WaitSSH = func(context.Context, string, session.RemoteConfig) error { return nil }
	}
	p, err := Provision(ctx, opts)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			_ = DestroyRemote(context.Background(), p.RemoteName)
		}
	})
	c, err := Lookup(mustLoad(t), "docker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx, p.Sandbox.ID); err != nil {
		t.Fatalf("Status: %v", err)
	}
	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, sb := range list {
		found = found || sb.ID == p.Sandbox.ID
	}
	if !found {
		t.Fatalf("List does not include %s: %+v", p.Sandbox.ID, list)
	}
	if err := DestroyRemote(ctx, p.RemoteName); err != nil {
		t.Fatalf("DestroyRemote: %v", err)
	}
	destroyed = true
}

func mustLoad(t *testing.T) *session.UserConfig {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
