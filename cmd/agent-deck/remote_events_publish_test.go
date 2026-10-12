package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
		t.Fatal("publish must forward through RunIO with stdin, never as a read")
	}
	// Refused kinds never reach config or SSH.
	code, err := runRemoteExec("no-such-remote", []string{"events", "publish", "--kind", "macapp.browser.request", "--data-file", "-"})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "macapp.browser.request") {
		t.Fatalf("request kind: code=%d err=%v", code, err)
	}
}

func TestRemoteEventsPublishDataCap(t *testing.T) {
	payload := []byte(`{"v":1,"req_id":"r","ok":true}`)
	r, err := readRemotePublishData(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(r); !bytes.Equal(got, payload) {
		t.Fatalf("passthrough: %q", got)
	}
	full := bytes.Repeat([]byte("a"), remotePublishDataCap)
	if r, err := readRemotePublishData(bytes.NewReader(full)); err != nil {
		t.Fatalf("payload at the cap refused: %v", err)
	} else if got, _ := io.ReadAll(r); len(got) != remotePublishDataCap {
		t.Fatalf("payload at the cap truncated: %d", len(got))
	}
	if _, err := readRemotePublishData(bytes.NewReader(append(full, 'a'))); err == nil {
		t.Fatal("payload over 2 MiB accepted")
	}
}

// fakePublishRemoteSSH puts an `ssh` shim first on PATH that records the
// remote command and the stdin it receives.
func fakePublishRemoteSSH(t *testing.T) (callLog, stdinLog string) {
	t.Helper()
	shim := t.TempDir()
	callLog = filepath.Join(shim, "calls.log")
	stdinLog = filepath.Join(shim, "stdin.log")
	script := `#!/bin/sh
for cmd; do :; done
printf '%s\n' "$cmd" >> '` + callLog + `'
cat > '` + stdinLog + `'
exit 0
`
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog, stdinLog
}

// T7 (CLI): the reply payload travels on stdin unchanged; a refused kind
// never starts ssh.
func TestRemoteEventsPublishCLIStdinPassthrough(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'fake-host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	callLog, stdinLog := fakePublishRemoteSSH(t)

	stdout, stderr, code := runAgentDeckStdin(t, home, "", "remote", "lab", "events", "publish", "--kind", "macapp.browser.request", "--session", "s1", "--data-file", "-")
	if code != 2 {
		t.Fatalf("request kind: exit=%d %s %s", code, stdout, stderr)
	}
	if _, err := os.Stat(callLog); !os.IsNotExist(err) {
		t.Fatalf("ssh ran for a refused kind: %v", err)
	}

	payload := `{"v":1,"req_id":"r1","request_event_id":"e1","ok":true,"result":{"text":"x"},"driver":"agent","app_id":"mac"}`
	stdout, stderr, code = runAgentDeckStdin(t, home, payload, "remote", "lab", "events", "publish", "--kind", "macapp.browser.response", "--session", "s1", "--data-file", "-")
	if code != 0 {
		t.Fatalf("reply: exit=%d %s %s", code, stdout, stderr)
	}
	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "'events' 'publish' '--kind' 'macapp.browser.response' '--session' 's1' '--data-file' '-'") {
		t.Fatalf("remote command: %s", calls)
	}
	if got, _ := os.ReadFile(stdinLog); string(got) != payload {
		t.Fatalf("stdin not forwarded unchanged: %q", got)
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
