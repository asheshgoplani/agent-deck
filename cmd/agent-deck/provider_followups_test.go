package main

// #2918 follow-ups to sandbox providers v1: `remote remove` on a sandbox
// remote, launch flag values that look like --provider/--name, and a signal
// arriving while provider work (create, rollback, destroy) is running.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteRemove_SandboxRemote(t *testing.T) {
	tests := []struct {
		name        string
		tty         bool
		answer      string
		wantCode    int
		wantRemoved bool
		wantOut     []string
	}{
		{name: "non-tty removes and points to destroy", wantRemoved: true,
			wantOut: []string{"still running", "agent-deck remote destroy sb1"}},
		{name: "tty no keeps the remote", tty: true, answer: "n\n", wantCode: 1,
			wantOut: []string{"still running", "agent-deck remote destroy sb1", "Nothing was changed"}},
		{name: "tty yes removes", tty: true, answer: "y\n", wantRemoved: true,
			wantOut: []string{"still running"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newProviderTestEnv(t, nil)
			rc := provisionForTest(t, env, "sb1")
			providerDeps.isTTY = func() bool { return tt.tty }
			providerDeps.stdin = strings.NewReader(tt.answer)
			if code := runRemoteRemove([]string{"sb1"}); code != tt.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tt.wantCode, env.stdout, env.stderr)
			}
			_, still := currentRemotes(t)["sb1"]
			if still == tt.wantRemoved {
				t.Fatalf("remote still registered = %v, want removed = %v", still, tt.wantRemoved)
			}
			out := env.stdout.String() + env.stderr.String()
			for _, want := range append(tt.wantOut, rc.SandboxID) {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if len(env.lines("destroyed.log")) != 0 {
				t.Fatal("remote remove destroyed the sandbox")
			}
		})
	}
}

func TestRemoteRemove_PlainRemoteUnchanged(t *testing.T) {
	env := newProviderTestEnv(t, map[string]session.RemoteConfig{"plain": {Host: "me@box"}})
	providerDeps.isTTY = func() bool { return true } // would prompt if it treated it as a sandbox
	if code := runRemoteRemove([]string{"plain"}); code != 0 {
		t.Fatalf("exit %d\nstderr: %s", code, env.stderr)
	}
	if _, ok := currentRemotes(t)["plain"]; ok {
		t.Fatal("plain remote not removed")
	}
	if out := env.stdout.String() + env.stderr.String(); strings.Contains(out, "destroy") || !strings.Contains(out, "Removed remote 'plain'") {
		t.Fatalf("plain remove output = %q", out)
	}
}

// A launch flag's value is the user's text, never --provider or --name.
func TestSplitLaunchProviderArgs_LeavesLaunchFlagValues(t *testing.T) {
	tests := []struct {
		args               []string
		wantOK             bool
		wantProvider, name string
		wantRest           string
	}{
		{args: []string{"/p", "-m", "--provider=fake"}, wantRest: "/p -m --provider=fake"},
		{args: []string{"/p", "--message", "--provider"}, wantRest: "/p --message --provider"},
		{args: []string{"-t", "--name", "/p"}, wantRest: "-t --name /p"},
		{args: []string{"--provider", "fake", "/p", "-m", "--name", "-c", "claude"}, wantOK: true, wantProvider: "fake",
			wantRest: "/p -m --name -c claude"},
		{args: []string{"--provider", "fake", "--name=x1", "/p", "-m=--name"}, wantOK: true, wantProvider: "fake", name: "x1",
			wantRest: "/p -m=--name"},
	}
	for _, tt := range tests {
		prov, name, rest, ok, err := splitLaunchProviderArgs(tt.args)
		if err != nil || ok != tt.wantOK || prov != tt.wantProvider || name != tt.name || strings.Join(rest, " ") != tt.wantRest {
			t.Errorf("split(%q) = provider %q name %q rest %q ok %v err %v; want provider %q name %q rest %q ok %v",
				tt.args, prov, name, rest, ok, err, tt.wantProvider, tt.name, tt.wantRest, tt.wantOK)
		}
	}
}

func TestAwaitRemoteSignalExit(t *testing.T) {
	const grace = 20 * time.Millisecond
	run := func(sigs <-chan os.Signal, w *providerWorkTracker, maxWait time.Duration) <-chan struct{} {
		done := make(chan struct{})
		go func() { awaitRemoteSignalExit(sigs, w, grace, maxWait); close(done) }()
		return done
	}
	t.Run("no provider work: exits after the grace", func(t *testing.T) {
		select {
		case <-run(make(chan os.Signal, 1), &providerWorkTracker{}, time.Minute):
		case <-time.After(5 * time.Second):
			t.Fatal("did not exit")
		}
	})
	t.Run("waits for provider work to finish", func(t *testing.T) {
		w := &providerWorkTracker{}
		finish := w.begin()
		done := run(make(chan os.Signal, 1), w, time.Minute)
		select {
		case <-done:
			t.Fatal("exited while provider work (rollback or destroy) was running")
		case <-time.After(20 * grace):
		}
		finish()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("did not exit after the provider work finished")
		}
	})
	t.Run("second signal forces exit", func(t *testing.T) {
		w := &providerWorkTracker{}
		defer w.begin()()
		sigs := make(chan os.Signal, 1)
		done := run(sigs, w, time.Minute)
		sigs <- os.Interrupt
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("second signal did not force exit")
		}
	})
	t.Run("wait is bounded", func(t *testing.T) {
		w := &providerWorkTracker{}
		defer w.begin()()
		select {
		case <-run(make(chan os.Signal, 1), w, 100*time.Millisecond):
		case <-time.After(5 * time.Second):
			t.Fatal("wait for provider work is not bounded")
		}
	})
}

// Provisioning and destroying mark provider work, so a signal waits for them.
func TestProviderCommandsMarkProviderWork(t *testing.T) {
	env := newProviderTestEnv(t, nil)
	var duringProvision, duringDestroy bool
	providerDeps.waitSSH = func(context.Context, string, session.RemoteConfig) error {
		duringProvision = providerWork.busy()
		return nil
	}
	provisionForTest(t, env, "sb2")
	providerDeps.listSessions = func(context.Context, string, session.RemoteConfig) ([]session.RemoteSessionInfo, error) {
		duringDestroy = providerWork.busy()
		return nil, nil
	}
	if code := runRemoteDestroy([]string{"sb2", "--yes"}); code != 0 {
		t.Fatalf("destroy exit %d\nstderr: %s", code, env.stderr)
	}
	if !duringProvision || !duringDestroy {
		t.Fatalf("provider work marked during provision=%v destroy=%v, want both", duringProvision, duringDestroy)
	}
	if providerWork.busy() {
		t.Fatal("provider work still marked after the commands returned")
	}
}
