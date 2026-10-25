package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func okSeam(context.Context, string, session.RemoteConfig) error { return nil }

func loadRemotes(t *testing.T) map[string]session.RemoteConfig {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Remotes
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestProvisionRegistersSandboxRemote(t *testing.T) {
	freshHome(t)
	configureFake(t)
	state := fakeEnv(t, "ok")
	var waited, installed session.RemoteConfig
	var progress []string
	p, err := Provision(context.Background(), ProvisionOptions{
		Provider: "fake", RemoteName: "box1", Profile: "work",
		Progress: func(s string) { progress = append(progress, s) },
		WaitSSH:  func(_ context.Context, _ string, rc session.RemoteConfig) error { waited = rc; return nil },
		Install:  func(_ context.Context, _ string, rc session.RemoteConfig) error { installed = rc; return nil },
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if p.RemoteName != "box1" || p.Sandbox.ID == "" {
		t.Fatalf("Provisioned = %+v", p)
	}
	rc, ok := loadRemotes(t)["box1"]
	if !ok {
		t.Fatal("remote not registered")
	}
	if rc.Host != "agentdeck-sb-box1" || rc.Provider != "fake" || rc.SandboxID != p.Sandbox.ID || rc.SSHConfig == "" {
		t.Fatalf("registered remote = %+v", rc)
	}
	if waited.SSHConfig != rc.SSHConfig || installed.SSHConfig != rc.SSHConfig {
		t.Fatalf("seams got %+v / %+v", waited, installed)
	}
	if !IsSandbox(rc) || DisplayKind(rc) != "sandbox (provider fake)" {
		t.Fatalf("IsSandbox/DisplayKind wrong for %+v", rc)
	}
	if IsSandbox(session.RemoteConfig{Host: "me@box"}) || DisplayKind(session.RemoteConfig{Host: "me@box"}) != "" {
		t.Fatal("plain remote reported as sandbox")
	}
	if len(progress) == 0 {
		t.Fatal("no progress reported")
	}

	dir := filepath.Dir(rc.SSHConfig)
	assertMode(t, dir, 0o700)
	assertMode(t, rc.SSHConfig, 0o600)
	assertMode(t, filepath.Join(dir, "known_hosts"), 0o600)
	assertMode(t, filepath.Join(dir, "sandbox.json"), 0o600)
	data, err := os.ReadFile(rc.SSHConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(data)
	for _, want := range []string{
		"Host agentdeck-sb-box1\n",
		"  HostName 127.0.0.1\n",
		"  Port 2222\n",
		"  User dev\n",
		"  IdentityFile \"" + filepath.Join(state, "id_ed25519") + "\"\n",
		"  IdentitiesOnly yes\n",
		"  UserKnownHostsFile \"" + filepath.Join(dir, "known_hosts") + "\"\n",
		"  StrictHostKeyChecking accept-new\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ssh_config lacks %q:\n%s", want, cfg)
		}
	}
	reqs := strings.Join(readLines(t, filepath.Join(state, "requests.log")), "\n")
	if !strings.Contains(reqs, `"agent-deck/profile":"work"`) || !strings.Contains(reqs, `"agent-deck/remote":"box1"`) {
		t.Fatalf("create labels missing: %s", reqs)
	}
}

func TestProvisionDefaultNameUsesProviderPrefix(t *testing.T) {
	freshHome(t)
	configureFake(t)
	fakeEnv(t, "ok")
	p, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", WaitSSH: okSeam, Install: okSeam})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.RemoteName, "fake-") || strings.ContainsAny(p.RemoteName, " /\\.:") {
		t.Fatalf("default remote name %q", p.RemoteName)
	}
}

// Any failure after create rolls back: the sandbox is destroyed, the remote
// unregistered and the sandbox files removed, and both errors are reported.
func TestProvisionRollsBackWhenAStepAfterCreateFails(t *testing.T) {
	for _, step := range []string{"wait", "install"} {
		t.Run(step, func(t *testing.T) {
			freshHome(t)
			configureFake(t)
			state := fakeEnv(t, "ok")
			fail := func(context.Context, string, session.RemoteConfig) error { return errors.New(step + " exploded") }
			opts := ProvisionOptions{Provider: "fake", RemoteName: "rb1", WaitSSH: okSeam, Install: okSeam}
			if step == "wait" {
				opts.WaitSSH = fail
			} else {
				opts.Install = fail
			}
			_, err := Provision(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), step+" exploded") {
				t.Fatalf("err = %v", err)
			}
			created := readLines(t, filepath.Join(state, "created.log"))
			destroyed := readLines(t, filepath.Join(state, "destroyed.log"))
			if len(created) != 1 || len(destroyed) != 1 || created[0] != destroyed[0] {
				t.Fatalf("rollback did not destroy the sandbox: created=%q destroyed=%q", created, destroyed)
			}
			if _, ok := loadRemotes(t)["rb1"]; ok {
				t.Fatal("remote still registered after rollback")
			}
			dir, _ := SandboxDir("rb1")
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("sandbox dir left behind: %v", err)
			}
		})
	}
}

func TestProvisionRollbackReportsDestroyFailure(t *testing.T) {
	freshHome(t)
	configureFake(t)
	fakeEnv(t, "ok")
	t.Setenv("FAKE_PROVIDER_DESTROY", "fail")
	_, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "rb2", Install: okSeam,
		WaitSSH: func(context.Context, string, session.RemoteConfig) error { return errors.New("ssh never came up") }})
	if err == nil || !strings.Contains(err.Error(), "ssh never came up") || !strings.Contains(err.Error(), "destroy refused by fake") {
		t.Fatalf("err should report both failures: %v", err)
	}
}

// Invalid provider output after a successful create still destroys the
// sandbox the provider made.
func TestProvisionInvalidSandboxIsDestroyed(t *testing.T) {
	freshHome(t)
	configureFake(t)
	state := fakeEnv(t, "badhost")
	_, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "bad1", WaitSSH: okSeam, Install: okSeam})
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("err = %v", err)
	}
	// badhost mode answers every action with the bad host but destroy works.
	_ = state
	if _, ok := loadRemotes(t)["bad1"]; ok {
		t.Fatal("invalid sandbox registered")
	}
	if _, err := os.Stat(filepath.Join(state, "pwned")); err == nil {
		t.Fatal("host option injection ran a command")
	}
}

func TestProvisionRefusesExistingOrInvalidName(t *testing.T) {
	freshHome(t)
	cfg := &session.UserConfig{
		Providers: map[string]session.ProviderConfig{"fake": {Command: fakeProviderPath(t)}},
		Remotes:   map[string]session.RemoteConfig{"taken": {Host: "me@box"}},
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state := fakeEnv(t, "ok")
	for _, name := range []string{"taken", "has space", "a.b", "x:y", "a@b", "" + "-lead"} {
		if _, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: name, WaitSSH: okSeam, Install: okSeam}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if len(readLines(t, filepath.Join(state, "created.log"))) != 0 {
		t.Fatal("a sandbox was created for a refused name")
	}
	if _, err := Provision(context.Background(), ProvisionOptions{Provider: "nope", RemoteName: "n1"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestPinnedHostKeysUseStrictChecking(t *testing.T) {
	freshHome(t)
	sb := validSandbox(t)
	sb.HostKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}
	if err := sb.Validate(); err != nil {
		t.Fatal(err)
	}
	cfgPath, err := WriteSandboxFiles("pin1", &sb)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "StrictHostKeyChecking yes") {
		t.Fatalf("pinned keys should use strict checking:\n%s", data)
	}
	kh, _ := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "known_hosts"))
	if string(kh) != "[127.0.0.1]:2222 "+sb.HostKeys[0]+"\n" {
		t.Fatalf("known_hosts = %q", kh)
	}
	if err := RemoveSandboxFiles("pin1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(cfgPath)); !os.IsNotExist(err) {
		t.Fatal("sandbox dir not removed")
	}
}

func TestDestroyRemote(t *testing.T) {
	freshHome(t)
	configureFake(t)
	state := fakeEnv(t, "ok")
	p, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "d1", WaitSSH: okSeam, Install: okSeam})
	if err != nil {
		t.Fatal(err)
	}
	if err := DestroyRemote(context.Background(), "d1"); err != nil {
		t.Fatalf("DestroyRemote: %v", err)
	}
	if got := readLines(t, filepath.Join(state, "destroyed.log")); len(got) != 1 || got[0] != p.Sandbox.ID {
		t.Fatalf("destroyed = %q", got)
	}
	if _, ok := loadRemotes(t)["d1"]; ok {
		t.Fatal("remote still registered")
	}
	if _, err := os.Stat(filepath.Dir(p.Remote.SSHConfig)); !os.IsNotExist(err) {
		t.Fatal("sandbox files left behind")
	}
}

func TestDestroyRemoteKeepsRegistrationWhenProviderFails(t *testing.T) {
	freshHome(t)
	configureFake(t)
	fakeEnv(t, "ok")
	if _, err := Provision(context.Background(), ProvisionOptions{Provider: "fake", RemoteName: "d2", WaitSSH: okSeam, Install: okSeam}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PROVIDER_DESTROY", "fail")
	if err := DestroyRemote(context.Background(), "d2"); err == nil {
		t.Fatal("expected destroy failure")
	}
	if _, ok := loadRemotes(t)["d2"]; !ok {
		t.Fatal("remote unregistered although the sandbox still exists")
	}
}

func TestDestroyRemoteRejectsPlainRemote(t *testing.T) {
	freshHome(t)
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"plain": {Host: "me@box"}}}); err != nil {
		t.Fatal(err)
	}
	err := DestroyRemote(context.Background(), "plain")
	if err == nil || !strings.Contains(err.Error(), "remote remove") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := loadRemotes(t)["plain"]; !ok {
		t.Fatal("plain remote removed")
	}
}
