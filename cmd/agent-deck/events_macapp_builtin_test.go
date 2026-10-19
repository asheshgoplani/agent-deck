package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// T6: the Mac app's own frames (browser response, focus claim) publish
// without [macapp] plugins, like macapp.open and its ack. A browser request
// drives the owner's browser and needs the opt-in, as do the host's
// session.closed and the plugin kinds (including the local browser plugin
// kinds).
func TestEventsPublishBrowserKindsUngated(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = false\n")
	for _, kind := range []string{events.KindMacappBrowserResponse, events.KindMacappAppFocus, events.KindMacappOpen, events.KindMacappOpenAck} {
		out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", kind, "--session", "s", "--data", `{"v":1}`)
		if !builtinPublishAdmitted(code, out+stderr) {
			t.Fatalf("%s gated: %d %s %s", kind, code, out, stderr)
		}
	}
	// A browser request is refused first by the [browser] enabled opt-in
	// (TestEventsPublishBrowserRequestNeedsBrowserEnabled), then by plugins.
	if out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", events.KindMacappBrowserRequest); code != 2 || !strings.Contains(out+stderr, "browser.enabled") {
		t.Fatalf("browser request: %d %s %s", code, out, stderr)
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = false\n\n[browser]\nenabled = true\n")
	for _, kind := range []string{events.KindMacappBrowserRequest, events.KindMacappSessionClosed, "macapp.browser.open", "macapp.browser.reload", "macapp.browser", "macapp.app.focus.other", "macapp.session.closed.x"} {
		out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", kind)
		if code != 2 || !strings.Contains(out+stderr, "plugins = true") {
			t.Fatalf("plugin gate %s: %d %s %s", kind, code, out, stderr)
		}
	}
}

// C6: a forwarded `remote <box> events ...` runs with the remote config's
// fixed -p, so `events publish|follow --profile` is how a caller reaches
// another existing profile's log on that host. A missing profile is refused
// and never creates a bus.
func TestEventsPublishAndFollowProfileFlag(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "-p", "second", "add", t.TempDir(), "--title", "Second", "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add in second profile: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "events", "publish", "--profile", "second", "--kind", events.KindMacappOpenAck, "--session", "s2", "--data", `{"open_event_id":"e","opened":true}`, "--json")
	var res struct {
		Profile string `json:"profile"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil || res.Profile != "second" {
		t.Fatalf("publish --profile second: exit=%d result=%s %s", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappOpenAck]; got != 0 {
		t.Fatalf("frame landed on the process profile: %d", got)
	}

	cmd := exec.Command(channelsCLIBinary(t), "events", "follow", "--jsonl", "--profile", "second", "--kind", "macapp.")
	cmd.Env = agentDeckTestEnv(home, nil)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	select {
	case line := <-lines:
		var frame events.Frame
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame.Kind != events.KindMacappOpenAck || frame.SessionID != "s2" {
			t.Fatalf("follow --profile frame: %s %v", line, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow --profile never replayed the second profile's frame")
	}

	stdout, stderr, code = runAgentDeck(t, home, "events", "publish", "--profile", "nosuch", "--kind", events.KindMacappOpenAck, "--data", `{}`)
	if code != 2 || !strings.Contains(stdout+stderr, "does not exist") {
		t.Fatalf("publish to a missing profile: %d %s %s", code, stdout, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	missing := exec.CommandContext(ctx, channelsCLIBinary(t), "events", "follow", "--jsonl", "--profile", "nosuch")
	missing.Env = agentDeckTestEnv(home, nil)
	out, err := missing.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "does not exist") {
		t.Fatalf("follow of a missing profile: %v %s", err, out)
	}
}

// m3: --profile takes a profile name, never a path. "a/second" would pass a
// base-name existence check and then open a disabled bus (publish: "bus is
// disabled", follow: nothing, forever).
func TestEventsProfileFlagRefusesPaths(t *testing.T) {
	home := t.TempDir()
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "second", "add", t.TempDir(), "--title", "Second", "-c", "bash", "--no-parent", "--json"); code != 0 {
		t.Fatalf("add in second profile: %d %s %s", code, stdout, stderr)
	}
	for _, name := range []string{"a/second", "../second", "./second"} {
		stdout, stderr, code := runAgentDeck(t, home, "events", "publish", "--profile", name, "--kind", events.KindMacappOpenAck, "--data", `{}`)
		if code != 2 || !strings.Contains(stdout+stderr, "not a path") {
			t.Fatalf("publish --profile %s: %d %s %s", name, code, stdout, stderr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		follow := exec.CommandContext(ctx, channelsCLIBinary(t), "events", "follow", "--jsonl", "--profile", name)
		follow.Env = agentDeckTestEnv(home, nil)
		out, err := follow.CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		if err == nil || timedOut || !strings.Contains(string(out), "not a path") {
			t.Fatalf("follow --profile %s: %v %s", name, err, out)
		}
	}
}

// Kind names are lowercase. A mixed-case spelling of a gated kind (such as
// macapp.Browser.Request) must not slip past an exact-match gate as an
// "unknown" kind: publish refuses it before any gate is consulted.
func TestEventsPublishRefusesNonLowercaseKind(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	for _, kind := range []string{"macapp.Browser.Request", "MACAPP.open", "macapp.Open.Ack", "macapp.session.Closed"} {
		out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", kind, "--session", "s", "--data", `{"v":1}`)
		if code != 2 || !strings.Contains(out+stderr, "lowercase") {
			t.Fatalf("%s: exit=%d %s %s, want exit 2 naming lowercase", kind, code, out, stderr)
		}
	}
	if got := openTestBusDir(t, home).Kinds; len(got) != 0 {
		t.Fatalf("a mixed-case kind was published: %v", got)
	}
}
