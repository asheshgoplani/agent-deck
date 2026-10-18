package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// T7: `remote <name> events publish` forwards only the Mac app's reply kinds,
// with the payload on stdin; every other kind, inline data and controller
// paths are refused before SSH.
func TestRemoteEventsPublishAllowlist(t *testing.T) {
	for _, args := range [][]string{
		{"events", "publish", "--kind", "macapp.open.ack", "--session", "s1", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.browser.response", "--session", "s1", "--data-file", "-", "--json"},
		{"events", "publish", "--kind=macapp.app.focus", "--data-file=-", "--profile", "work"},
		{"events", "publish", "--help"},
		{"events", "publish", "-h"},
		{"events", "follow", "--jsonl", "--since", "7", "--profile", "work"},
	} {
		got, err := remoteCommandArgs(args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("forward %q: %q %v", args, got, err)
		}
	}
	for _, args := range [][]string{
		{"events", "publish", "--kind", "macapp.browser.request", "--session", "s1", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.open", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.session.closed", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.canvas.show", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.browser.response.x", "--data-file", "-"},
		{"events", "publish", "--data-file", "-"},
		{"events", "publish", "--kind", "macapp.browser.response"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data", `{"ok":true}`},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "/etc/passwd"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "-", "extra"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "-", "--json=true"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "-", "--bus", "comms"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file"},
		{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "-", "--from-remote"},
		// A help flag never hides in a value: it would skip the payload cap.
		{"events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--session", "-h"},
		{"events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--session=--help"},
		{"events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--profile", "-x"},
		{"events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--help"},
		// The last --kind wins in Go flags, so a smuggled request kind is refused.
		{"events", "publish", "--kind", "macapp.browser.response", "--kind", "macapp.browser.request", "--data-file", "-"},
		{"events", "stats"},
	} {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	publish := []string{"events", "publish", "--kind", "macapp.browser.response", "--data-file", "-"}
	if !isRemoteEventsPublish(publish) || isRemoteReadCapability(publish) || isRemoteEventsPublish([]string{"events", "publish", "--help"}) {
		t.Fatal("publish must forward with its payload, never as a read")
	}
	// Refused kinds never reach config or SSH.
	code, err := runRemoteExec("no-such-remote", []string{"events", "publish", "--kind", "macapp.browser.request", "--data-file", "-"})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "macapp.browser.request") {
		t.Fatalf("request kind: code=%d err=%v", code, err)
	}
}

func TestRemoteEventsPublishDataCap(t *testing.T) {
	payload := []byte(`{"v":1,"req_id":"r","ok":true}`)
	got, err := readRemotePublishData(bytes.NewReader(payload))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("passthrough: %q %v", got, err)
	}
	full := bytes.Repeat([]byte("a"), remotePublishDataCap)
	if got, err := readRemotePublishData(bytes.NewReader(full)); err != nil || len(got) != remotePublishDataCap {
		t.Fatalf("payload at the cap: %d %v", len(got), err)
	}
	if _, err := readRemotePublishData(bytes.NewReader(append(full, 'a'))); err == nil {
		t.Fatal("payload over 2 MiB accepted")
	}
}

// remotePublishFixture is a registered remote "lab" whose `ssh` shim runs the
// remote command locally with sh, against a fake box agent-deck script.
type remotePublishFixture struct {
	home, callLog, boxArgs, boxStdin string
}

// newRemotePublishFixture writes the shim and the box script. box is the
// body of the fake box agent-deck (POSIX sh); it may use $ARGS_LOG and
// $STDIN_LOG.
func newRemotePublishFixture(t *testing.T, box string) remotePublishFixture {
	t.Helper()
	dir := t.TempDir()
	f := remotePublishFixture{
		home:     t.TempDir(),
		callLog:  filepath.Join(dir, "ssh-calls.log"),
		boxArgs:  filepath.Join(dir, "box-args.log"),
		boxStdin: filepath.Join(dir, "box-stdin.log"),
	}
	boxPath := filepath.Join(dir, "box-agent-deck")
	boxScript := "#!/bin/sh\nARGS_LOG='" + f.boxArgs + "'\nSTDIN_LOG='" + f.boxStdin + "'\n" + box
	if err := os.WriteFile(boxPath, []byte(boxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\nfor cmd; do :; done\nprintf '%s\\n' \"$cmd\" >> '" + f.callLog + "'\nexec sh -c \"$cmd\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := filepath.Join(f.home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'fake-host'\nagent_deck_path = '"+boxPath+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// A box with this change: answers the --help probe, then publishes.
const newBoxScript = `for a; do [ "$a" = --help ] && exit 0; done
printf '%s\n' "$*" >> "$ARGS_LOG"
cat > "$STDIN_LOG"
printf '{"ok":true}\n'
`

// A box from before this change: its events publish knows no --from-remote.
const oldBoxScript = `for a; do
  if [ "$a" = --from-remote ]; then echo "flag provided but not defined: -from-remote" >&2; exit 2; fi
done
for a; do [ "$a" = --help ] && exit 0; done
printf '%s\n' "$*" >> "$ARGS_LOG"
cat > "$STDIN_LOG"
echo "events publish is off: set [macapp] plugins = true" >&2
exit 2
`

// T7 (CLI): the reply payload travels on stdin unchanged, through the
// capability probe, with --from-remote so the box applies its own checks; a
// refused kind never starts ssh.
func TestRemoteEventsPublishCLIStdinPassthrough(t *testing.T) {
	f := newRemotePublishFixture(t, newBoxScript)
	stdout, stderr, code := runAgentDeckStdin(t, f.home, "", "remote", "lab", "events", "publish", "--kind", "macapp.browser.request", "--session", "s1", "--data-file", "-")
	if code != 2 {
		t.Fatalf("request kind: exit=%d %s %s", code, stdout, stderr)
	}
	if _, err := os.Stat(f.callLog); !os.IsNotExist(err) {
		t.Fatalf("ssh ran for a refused kind: %v", err)
	}

	payload := `{"v":1,"req_id":"r1","request_event_id":"e1","ok":true,"result":{"text":"x"},"driver":"agent","app_id":"mac"}`
	stdout, stderr, code = runAgentDeckStdin(t, f.home, payload, "remote", "lab", "events", "publish", "--kind", "macapp.browser.response", "--session", "s1", "--data-file", "-")
	if code != 0 || !strings.Contains(stdout, `{"ok":true}`) {
		t.Fatalf("reply: exit=%d %q %s", code, stdout, stderr)
	}
	boxArgs, _ := os.ReadFile(f.boxArgs)
	if got := strings.TrimSpace(string(boxArgs)); got != "events publish --kind macapp.browser.response --session s1 --data-file - --from-remote" {
		t.Fatalf("box command: %q", got)
	}
	if got, _ := os.ReadFile(f.boxStdin); string(got) != payload {
		t.Fatalf("stdin not forwarded unchanged: %q", got)
	}
}

// M2: a box that predates the reply publish fails the capability probe, so
// the controller answers "unsupported remote command" (the shape clients
// detect) instead of the old box's flag or gate error, and the old box never
// runs the publish.
func TestRemoteEventsPublishOldBoxUnsupported(t *testing.T) {
	f := newRemotePublishFixture(t, oldBoxScript)
	stdout, stderr, code := runAgentDeckStdin(t, f.home, `{"ok":true}`, "remote", "lab", "events", "publish", "--kind", "macapp.open.ack", "--session", "s1", "--data-file", "-")
	if code == 0 || !strings.Contains(stdout+stderr, `unsupported remote command "events publish" on remote "lab"`) {
		t.Fatalf("old box: exit=%d %q %q", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "flag provided but not defined") || strings.Contains(stdout+stderr, "plugins = true") {
		t.Fatalf("old box error leaked instead of unsupported: %q %q", stdout, stderr)
	}
	if _, err := os.Stat(f.boxStdin); !os.IsNotExist(err) {
		t.Fatalf("old box ran the publish: %v", err)
	}
	stdout, _, _ = runAgentDeckStdin(t, f.home, `{"ok":true}`, "remote", "lab", "events", "publish", "--kind", "macapp.open.ack", "--session", "s1", "--data-file", "-", "--json")
	if !strings.Contains(stdout, `"error":"unsupported remote command`) || !strings.Contains(stdout, `"remote":"lab"`) {
		t.Fatalf("old box --json answer: %q", stdout)
	}
}

// m2: the controller's 2 MiB cap holds for every forwarded publish: a help
// flag hidden in a value is refused, and an oversized payload never starts
// ssh.
func TestRemoteEventsPublishCapBeforeSSH(t *testing.T) {
	f := newRemotePublishFixture(t, newBoxScript)
	big := strings.Repeat("a", remotePublishDataCap+1)
	for _, args := range [][]string{
		{"remote", "lab", "events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--session", "-h"},
		{"remote", "lab", "events", "publish", "--kind", "macapp.open.ack", "--data-file", "-", "--session", "s1"},
	} {
		stdout, stderr, code := runAgentDeckStdin(t, f.home, big, args...)
		if code != 2 {
			t.Fatalf("%q: exit=%d %s %s", args, code, stdout, stderr)
		}
		if _, err := os.Stat(f.callLog); !os.IsNotExist(err) {
			t.Fatalf("%q: ssh ran with an oversized or unchecked payload", args)
		}
	}
}

// T7: the box side caps the payload too, for stdin and files alike.
func TestReadPublishDataCap(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.json")
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(ok, bytes.Repeat([]byte(" "), remotePublishDataCap), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), remotePublishDataCap+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readPublishData(ok); err != nil || len(raw) != remotePublishDataCap {
		t.Fatalf("at the cap: %d %v", len(raw), err)
	}
	if _, err := readPublishData(big); err == nil {
		t.Fatal("over the cap accepted from a file")
	}
	f, err := os.Open(big)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stdin := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = stdin }()
	if _, err := readPublishData("-"); err == nil {
		t.Fatal("over the cap accepted from stdin")
	}
}

// SECURITY: a publish forwarded from a controller (--from-remote) is checked
// on the box: only reply kinds, data on stdin, and only a session of this
// host. A focus claim may carry no session.
func TestEventsPublishFromRemoteBoxChecks(t *testing.T) {
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Box session")
	reply := `{"open_event_id":"e","opened":true}`
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"request kind", reply, []string{"--kind", events.KindMacappBrowserRequest, "--session", id, "--data-file", "-"}, "browser.enabled"},
		{"open kind", reply, []string{"--kind", events.KindMacappOpen, "--session", id, "--data-file", "-"}, "not a Mac app reply"},
		{"other session", reply, []string{"--kind", events.KindMacappOpenAck, "--session", "not-on-this-box", "--data-file", "-"}, "not a session of this host"},
		{"no session", reply, []string{"--kind", events.KindMacappBrowserResponse, "--data-file", "-"}, "--session is required"},
		{"inline data", "", []string{"--kind", events.KindMacappOpenAck, "--session", id, "--data", reply}, "on stdin"},
	} {
		args := append([]string{"events", "publish", "--from-remote"}, c.args...)
		stdout, stderr, code := runAgentDeckStdin(t, home, c.stdin, args...)
		if code != 2 || !strings.Contains(stdout+stderr, c.want) {
			t.Fatalf("%s: exit=%d %s %s", c.name, code, stdout, stderr)
		}
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappOpenAck]; got != 0 {
		t.Fatalf("a refused forwarded publish landed: %d", got)
	}
	for _, args := range [][]string{
		{"--kind", events.KindMacappOpenAck, "--session", id, "--data-file", "-"},
		{"--kind", events.KindMacappAppFocus, "--data-file", "-"},
	} {
		all := append([]string{"events", "publish", "--from-remote"}, args...)
		stdout, stderr, code := runAgentDeckStdin(t, home, reply, all...)
		if !builtinPublishAdmitted(code, stdout+stderr) {
			t.Fatalf("%q: exit=%d %s %s", args, code, stdout, stderr)
		}
	}
}
