package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// providerTestEnv gives the test its own HOME/XDG, a config.toml with
// [providers.fake] (the fake provider script) plus extra remotes, a fresh
// fake-provider state dir, and stub seams so nothing runs real ssh.
type providerTestEnv struct {
	state   string
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	stops   []string
	execs   [][]string
	listErr error
	list    []session.RemoteSessionInfo
	order   []string
}

func fakeProviderScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "internal", "provider", "testdata", "fake-provider.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newProviderTestEnv(t *testing.T, remotes map[string]session.RemoteConfig) *providerTestEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if err := session.SaveUserConfig(&session.UserConfig{
		Providers: map[string]session.ProviderConfig{"fake": {Command: fakeProviderScript(t), TimeoutSeconds: 10, CreateTimeoutSeconds: 10}},
		Remotes:   remotes,
	}); err != nil {
		t.Fatal(err)
	}
	env := &providerTestEnv{state: t.TempDir(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	t.Setenv("FAKE_PROVIDER_STATE", env.state)
	t.Setenv("FAKE_PROVIDER_MODE", "ok")
	t.Setenv("FAKE_PROVIDER_DESTROY", "ok")

	orig := providerDeps
	t.Cleanup(func() { providerDeps = orig })
	providerDeps = providerCLIDeps{
		waitSSH: func(context.Context, string, session.RemoteConfig) error { return nil },
		install: func(context.Context, string, session.RemoteConfig) error { return nil },
		remoteExec: func(name string, args []string) (int, error) {
			env.execs = append(env.execs, append([]string{name}, args...))
			return 0, nil
		},
		listSessions: func(context.Context, string, session.RemoteConfig) ([]session.RemoteSessionInfo, error) {
			return env.list, env.listErr
		},
		stopSession: func(_ context.Context, _ string, _ session.RemoteConfig, id string) error {
			if len(env.lines("destroyed.log")) != 0 {
				env.order = append(env.order, "stop-after-destroy")
			}
			env.stops = append(env.stops, id)
			return nil
		},
		isTTY:  func() bool { return false },
		stdin:  strings.NewReader(""),
		stdout: env.stdout,
		stderr: env.stderr,
	}
	return env
}

func (e *providerTestEnv) lines(file string) []string {
	data, err := os.ReadFile(filepath.Join(e.state, file))
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func currentRemotes(t *testing.T) map[string]session.RemoteConfig {
	t.Helper()
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Remotes
}

func provisionForTest(t *testing.T, env *providerTestEnv, name string) session.RemoteConfig {
	t.Helper()
	if code := runRemoteAddProvider("work", []string{"--provider", "fake", "--name", name}); code != 0 {
		t.Fatalf("remote add --provider exit %d\nstdout: %s\nstderr: %s", code, env.stdout, env.stderr)
	}
	rc, ok := currentRemotes(t)[name]
	if !ok {
		t.Fatalf("remote %q not registered", name)
	}
	env.stdout.Reset()
	env.stderr.Reset()
	return rc
}

func TestRemoteAddProvider_RegistersSandbox(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	var installed string
	providerDeps.install = func(_ context.Context, name string, rc session.RemoteConfig) error {
		installed = name + " " + rc.SSHConfig
		return nil
	}
	rc := provisionForTest(t, env, "box1")
	if !provider.IsSandbox(rc) || rc.Host != "agentdeck-sb-box1" {
		t.Fatalf("registered %+v", rc)
	}
	if installed != "box1 "+rc.SSHConfig {
		t.Fatalf("install seam got %q", installed)
	}
	reqs := strings.Join(env.lines("requests.log"), "\n")
	if !strings.Contains(reqs, `"agent-deck/profile":"work"`) {
		t.Fatalf("create request lacks the profile label: %s", reqs)
	}
	for _, line := range env.lines("argv.log") {
		if line != "argc=1 [create]" {
			t.Fatalf("provider argv = %q", line)
		}
	}
}

func TestRemoteAddProvider_OutputMentionsRemote(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	if code := runRemoteAddProvider("", []string{"--provider", "fake", "--name", "box2"}); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr)
	}
	if out := env.stdout.String(); !strings.Contains(out, "Added remote 'box2'") || !strings.Contains(out, "sandbox (provider fake)") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRemoteAddProvider_RollsBackWhenInstallFails(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	providerDeps.install = func(context.Context, string, session.RemoteConfig) error {
		return errors.New("no binary for this arch")
	}
	if code := runRemoteAddProvider("", []string{"--provider", "fake", "--name", "rb"}); code == 0 {
		t.Fatal("expected failure")
	}
	if !strings.Contains(env.stderr.String(), "no binary for this arch") {
		t.Fatalf("stderr: %s", env.stderr)
	}
	created, destroyed := env.lines("created.log"), env.lines("destroyed.log")
	if len(created) != 1 || len(destroyed) != 1 || created[0] != destroyed[0] {
		t.Fatalf("created=%q destroyed=%q", created, destroyed)
	}
	if _, ok := currentRemotes(t)["rb"]; ok {
		t.Fatal("remote left registered after rollback")
	}
}

func TestRemoteAddProvider_RejectsBadNames(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{"taken": {Host: "me@box"}})
	for _, name := range []string{"taken", "list", "a.b"} {
		if code := runRemoteAddProvider("", []string{"--provider", "fake", "--name", name}); code == 0 {
			t.Errorf("name %q accepted", name)
		}
	}
	if len(env.lines("created.log")) != 0 {
		t.Fatal("sandbox created for a refused name")
	}
}

func TestLaunchProvider_ProvisionsThenLaunchesOnTheSandbox(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	code := runLaunchProvider("", []string{"--provider", "fake", "--name", "l1", "/work/proj", "-c", "claude", "-m", "hello there"})
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, env.stdout, env.stderr)
	}
	if len(env.execs) != 1 {
		t.Fatalf("remote exec calls = %q", env.execs)
	}
	want := []string{"l1", "launch", "/work/proj", "-c", "claude", "-m", "hello there"}
	if strings.Join(env.execs[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("remote exec = %q, want %q", env.execs[0], want)
	}
	if _, ok := currentRemotes(t)["l1"]; !ok {
		t.Fatal("launch --provider did not register the remote")
	}
}

func TestLaunchProvider_FlagForms(t *testing.T) {
	provName, name, rest, ok, err := splitLaunchProviderArgs([]string{"-c", "codex", "--provider=fake", "-name", "x1", "/p"})
	if err != nil || !ok || provName != "fake" || name != "x1" || strings.Join(rest, " ") != "-c codex /p" {
		t.Fatalf("got %q %q %q %v %v", provName, name, rest, ok, err)
	}
	if _, _, _, ok, _ := splitLaunchProviderArgs([]string{"-c", "claude", "/p"}); ok {
		t.Fatal("plain launch detected as provider launch")
	}
	if _, _, _, _, err := splitLaunchProviderArgs([]string{"--provider"}); err == nil {
		t.Fatal("missing --provider value accepted")
	}
	// After "--" everything belongs to the launch.
	if _, _, rest, ok, _ := splitLaunchProviderArgs([]string{"--provider", "fake", "/p", "--", "--name", "x"}); !ok || strings.Join(rest, " ") != "/p -- --name x" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestRemoteDestroy_RefusesNonTTYWithoutYes(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	rc := provisionForTest(t, env, "d1")
	env.list = []session.RemoteSessionInfo{{ID: "s1", Title: "one", Status: "running"}, {ID: "s2", Title: "two", Status: "idle"}}
	if code := runRemoteDestroy([]string{"d1"}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(env.stderr.String(), "--yes") {
		t.Fatalf("stderr should point at --yes: %s", env.stderr)
	}
	if len(env.stops) != 0 || len(env.lines("destroyed.log")) != 0 {
		t.Fatalf("changed something: stops=%q destroyed=%q", env.stops, env.lines("destroyed.log"))
	}
	if _, ok := currentRemotes(t)["d1"]; !ok {
		t.Fatal("remote unregistered")
	}
	if _, err := os.Stat(rc.SSHConfig); err != nil {
		t.Fatalf("sandbox files touched: %v", err)
	}
}

func TestRemoteDestroy_YesStopsThenDestroysThenUnregisters(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	rc := provisionForTest(t, env, "d2")
	env.list = []session.RemoteSessionInfo{{ID: "s1", Status: "running"}, {ID: "s2", Status: "waiting"}, {ID: "s3", Status: "stopped"}}
	if code := runRemoteDestroy([]string{"d2", "--yes"}); code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, env.stdout, env.stderr)
	}
	if strings.Join(env.stops, ",") != "s1,s2" {
		t.Fatalf("stopped %q, want s1,s2", env.stops)
	}
	if len(env.order) != 0 {
		t.Fatalf("a session was stopped after the sandbox was destroyed: %q", env.order)
	}
	if got := env.lines("destroyed.log"); len(got) != 1 || got[0] != rc.SandboxID {
		t.Fatalf("destroyed = %q", got)
	}
	if _, ok := currentRemotes(t)["d2"]; ok {
		t.Fatal("remote still registered")
	}
	if _, err := os.Stat(filepath.Dir(rc.SSHConfig)); !os.IsNotExist(err) {
		t.Fatal("sandbox files left behind")
	}
}

func TestRemoteDestroy_TTYPrompt(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	provisionForTest(t, env, "d3")
	env.list = []session.RemoteSessionInfo{{ID: "s1", Status: "running"}}
	providerDeps.isTTY = func() bool { return true }
	providerDeps.stdin = strings.NewReader("n\n")
	if code := runRemoteDestroy([]string{"d3"}); code != 1 {
		t.Fatalf("declined prompt exit %d", code)
	}
	if !strings.Contains(env.stdout.String(), "Stop 1 session on d3 and destroy the sandbox? [y/N]") {
		t.Fatalf("prompt missing: %s", env.stdout)
	}
	if len(env.stops) != 0 || len(env.lines("destroyed.log")) != 0 {
		t.Fatal("declined prompt changed something")
	}
	providerDeps.stdin = strings.NewReader("y\n")
	if code := runRemoteDestroy([]string{"d3"}); code != 0 {
		t.Fatalf("accepted prompt exit %d: %s", code, env.stderr)
	}
	if len(env.lines("destroyed.log")) != 1 {
		t.Fatal("accepted prompt did not destroy")
	}
}

func TestRemoteDestroy_NoSessionsNeedsNoConfirmation(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	provisionForTest(t, env, "d4")
	if code := runRemoteDestroy([]string{"d4"}); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr)
	}
	if len(env.lines("destroyed.log")) != 1 {
		t.Fatal("not destroyed")
	}
}

func TestRemoteDestroy_RejectsPlainRemote(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{"plain": {Host: "me@box"}})
	if code := runRemoteDestroy([]string{"plain", "--yes"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(env.stderr.String(), "remote remove") {
		t.Fatalf("stderr should suggest remote remove: %s", env.stderr)
	}
	if _, ok := currentRemotes(t)["plain"]; !ok {
		t.Fatal("plain remote removed")
	}
	if len(env.lines("argv.log")) != 0 {
		t.Fatal("provider was called for a plain remote")
	}
}

func TestRemoteDestroy_UnreachableRefusesUnlessForce(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	provisionForTest(t, env, "d5")
	env.listErr = errors.New("ssh: connect to host refused")
	if code := runRemoteDestroy([]string{"d5", "--yes"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(env.stderr.String(), "--force") || len(env.lines("destroyed.log")) != 0 {
		t.Fatalf("unreachable remote: stderr=%s destroyed=%q", env.stderr, env.lines("destroyed.log"))
	}
	if code := runRemoteDestroy([]string{"d5", "--force"}); code != 0 {
		t.Fatalf("--force exit %d: %s", code, env.stderr)
	}
	if len(env.lines("destroyed.log")) != 1 {
		t.Fatal("--force did not destroy")
	}
}

func TestRemoteList_ShowsSandboxKind(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{"plain": {Host: "me@box"}})
	provisionForTest(t, env, "k1")
	out := captureStdout(t, func() { handleRemoteList(nil) })
	if !strings.Contains(out, "sandbox (provider fake)") {
		t.Fatalf("remote list lacks the sandbox label:\n%s", out)
	}
	js := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
	if !strings.Contains(js, `"kind": "sandbox (provider fake)"`) || !strings.Contains(js, `"provider": "fake"`) {
		t.Fatalf("remote list --json lacks the sandbox fields:\n%s", js)
	}
	if strings.Count(js, `"kind"`) != 1 {
		t.Fatalf("plain remote got a kind:\n%s", js)
	}
}

func TestProviderTestCommand_CreateRoundTrip(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	if code := runProviderTest("work", []string{"fake", "--create"}); code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, env.stdout, env.stderr)
	}
	out := env.stdout.String()
	for _, step := range []string{"list", "create", "status", "destroy"} {
		if !strings.Contains(out, step+": ok") {
			t.Errorf("output lacks %q step:\n%s", step, out)
		}
	}
	if c, d := env.lines("created.log"), env.lines("destroyed.log"); len(c) != 1 || len(d) != 1 {
		t.Fatalf("created=%q destroyed=%q", c, d)
	}
	if len(currentRemotes(t)) != 0 {
		t.Fatal("provider test registered a remote")
	}
}

func TestProviderTestCommand_ReportsFailure(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	t.Setenv("FAKE_PROVIDER_MODE", "protocol2")
	if code := runProviderTest("", []string{"fake"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(env.stderr.String()+env.stdout.String(), "protocol 2") {
		t.Fatalf("output: %s %s", env.stdout, env.stderr)
	}
}

func TestProviderList(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	if code := runProviderList([]string{"--json"}); code != 0 {
		t.Fatalf("exit %d: %s", code, env.stderr)
	}
	out := env.stdout.String()
	if !strings.Contains(out, `"name": "fake"`) || !strings.Contains(out, `"reachable": true`) {
		t.Fatalf("provider list --json:\n%s", out)
	}
	env.stdout.Reset()
	if code := runProviderList(nil); code != 0 || !strings.Contains(env.stdout.String(), "fake") {
		t.Fatalf("provider list: %d %s", code, env.stdout)
	}
}
