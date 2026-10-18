package triggers

import (
	"errors"
	"testing"
)

// fakeEnv builds an inference environment from a variable map and a fake
// file system (paths under /proc).
func fakeEnv(vars map[string]string, files map[string]string, ppid int) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no such file")
		},
		PPID: ppid,
	}
}

const userCgroup = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/agentdeck-overnight.service\n"

func TestInferLaunchd(t *testing.T) {
	got := Infer(fakeEnv(map[string]string{"XPC_SERVICE_NAME": "com.ashesh.limit-guard"}, nil, 0))
	if got.Kind != KindLaunchd || got.Name != "com.ashesh.limit-guard" || !got.Inferred || got.ID() != "launchd:com.ashesh.limit-guard" {
		t.Fatalf("launchd: %+v", got)
	}
	// A terminal app's XPC name is a person at a shell, not a job.
	for _, v := range []string{"application.com.apple.Terminal.123", "0"} {
		if got := Infer(fakeEnv(map[string]string{"XPC_SERVICE_NAME": v}, nil, 0)); got.Kind != KindCLI {
			t.Fatalf("XPC_SERVICE_NAME=%q inferred %+v, want cli", v, got)
		}
	}
}

func TestInferSystemdNeedsInvocationIDAndCgroup(t *testing.T) {
	files := map[string]string{"/proc/self/cgroup": userCgroup}
	got := Infer(fakeEnv(map[string]string{"INVOCATION_ID": "6f1c"}, files, 0))
	if got.Kind != KindSystemd || got.Name != "agentdeck-overnight" || !got.Inferred {
		t.Fatalf("systemd: %+v", got)
	}
	// Without INVOCATION_ID the cgroup alone is a login shell's scope.
	if got := Infer(fakeEnv(nil, files, 0)); got.Kind != KindCLI {
		t.Fatalf("cgroup without INVOCATION_ID: %+v", got)
	}
	// The user manager itself is not a sender.
	// A system service (the CI runner's own unit) is not a trigger (#2738).
	sys := map[string]string{"/proc/self/cgroup": "0::/system.slice/hosted-compute-agent.service\n"}
	if got := Infer(fakeEnv(map[string]string{"INVOCATION_ID": "1"}, sys, 0)); got.Kind != KindCLI {
		t.Fatalf("system service cgroup: %+v", got)
	}
	mgr := map[string]string{"/proc/self/cgroup": "0::/user.slice/user-1000.slice/user@1000.service\n"}
	if got := Infer(fakeEnv(map[string]string{"INVOCATION_ID": "6f1c"}, mgr, 0)); got.Kind != KindCLI {
		t.Fatalf("user manager cgroup: %+v", got)
	}
}

func TestInferCronAncestorNamesTheScript(t *testing.T) {
	files := map[string]string{
		"/proc/300/stat":    "300 (bash) S 200 300 300 0 -1",
		"/proc/300/cmdline": "/bin/bash\x00/home/u/bin/gmail-poll.sh\x00",
		"/proc/200/stat":    "200 (sh) S 100 200 200 0 -1",
		"/proc/200/cmdline": "/bin/sh\x00-c\x00/home/u/bin/gmail-poll.sh\x00",
		"/proc/100/stat":    "100 (cron) S 1 100 100 0 -1",
		"/proc/100/cmdline": "/usr/sbin/cron\x00-f\x00",
	}
	got := Infer(fakeEnv(nil, files, 300))
	if got.Kind != KindCron || got.Name != "gmail-poll.sh" || !got.Inferred {
		t.Fatalf("cron: %+v", got)
	}
	// No cron among the ancestors: not cron.
	files["/proc/100/stat"] = "100 (sshd) S 1 100 100 0 -1"
	if got := Infer(fakeEnv(nil, files, 300)); got.Kind == KindCron {
		t.Fatalf("non-cron ancestry inferred cron: %+v", got)
	}
}

func TestInferSSHAndFallback(t *testing.T) {
	if got := Infer(fakeEnv(map[string]string{"SSH_CONNECTION": "10.0.0.2 5555 10.0.0.1 22"}, nil, 0)); got.Kind != KindSSH || got.ID() != "ssh:ssh" {
		t.Fatalf("ssh: %+v", got)
	}
	if got := Infer(fakeEnv(nil, nil, 0)); got.Kind != KindCLI || got.ID() != "cli:cli" || !got.Inferred {
		t.Fatalf("fallback: %+v", got)
	}
}

func TestResolveExplicitWins(t *testing.T) {
	env := fakeEnv(map[string]string{
		"XPC_SERVICE_NAME": "com.x.job",
		"AGENTDECK_SOURCE": "watcher:gh",
	}, nil, 0)
	got := Resolve("heartbeat:ops", "sess-1", env)
	if got.Kind != KindHeartbeat || got.Name != "ops" || got.Inferred || got.ID() != "heartbeat:ops" {
		t.Fatalf("explicit --source: %+v", got)
	}
	// AGENTDECK_SOURCE is next, ahead of the sending session and inference.
	got = Resolve("", "sess-1", env)
	if got.Kind != KindWatcher || got.Name != "gh" || got.Inferred {
		t.Fatalf("AGENTDECK_SOURCE: %+v", got)
	}
	// Then the sending session.
	got = Resolve("", "sess-1", fakeEnv(map[string]string{"XPC_SERVICE_NAME": "com.x.job"}, nil, 0))
	if got.Kind != KindSession || got.Name != "sess-1" || got.IsTrigger() {
		t.Fatalf("sender session: %+v", got)
	}
	// "cli" as a sender id is not a session.
	if got := Resolve("", "cli", fakeEnv(nil, nil, 0)); got.Kind != KindCLI || got.IsTrigger() {
		t.Fatalf("cli sender: %+v", got)
	}
}

// A bare --source name sent from a systemd unit or launchd job joins that
// unit's scanned row (same id) and keeps the explicit name for display.
func TestResolveBareNameJoinsUnit(t *testing.T) {
	env := fakeEnv(map[string]string{"INVOCATION_ID": "1"}, map[string]string{"/proc/self/cgroup": userCgroup}, 0)
	got := Resolve("overnight manager", "", env)
	if got.Kind != KindSystemd || got.Unit != "agentdeck-overnight" || got.Name != "overnight manager" || got.ID() != "systemd:agentdeck-overnight" {
		t.Fatalf("bare name under systemd: %+v", got)
	}
	got = Resolve("limit guard", "", fakeEnv(map[string]string{"XPC_SERVICE_NAME": "com.x.limit-guard"}, nil, 0))
	if got.ID() != "launchd:com.x.limit-guard" || got.Name != "limit guard" {
		t.Fatalf("bare name under launchd: %+v", got)
	}
	// A kind-qualified name never joins a unit.
	if got := Resolve("channel:telegram", "", env); got.ID() != "channel:telegram" {
		t.Fatalf("qualified name joined a unit: %+v", got)
	}
	// From a shell the bare name stands on its own.
	if got := Resolve("nightly", "", fakeEnv(nil, nil, 0)); got.ID() != "job:nightly" {
		t.Fatalf("bare name from a shell: %+v", got)
	}
}

func TestParseExplicit(t *testing.T) {
	cases := map[string]string{
		"heartbeat:ops":       "heartbeat:ops",
		"channel:telegram":    "channel:telegram",
		"interval:sweep":      "interval:sweep",
		"  watcher:gmail\n":   "watcher:gmail",
		"unknownkind:name":    "job:unknownkind:name",
		"poller":              "job:poller",
		"heartbeat:":          "job:heartbeat:",
		"command-center":      "channel:command-center",
		"bad\x1b[31mname\x07": "job:bad[31mname",
	}
	for in, want := range cases {
		if got := ParseExplicit(in).ID(); got != want {
			t.Errorf("ParseExplicit(%q) = %q, want %q", in, got, want)
		}
	}
	// The command center is a channel even when the web server runs under
	// a systemd unit: it never joins that unit's row.
	env := fakeEnv(map[string]string{"INVOCATION_ID": "1"}, map[string]string{"/proc/self/cgroup": userCgroup}, 0)
	if got := Resolve("command-center", "", env); got.ID() != "channel:command-center" {
		t.Fatalf("command-center under systemd: %+v", got)
	}
}
