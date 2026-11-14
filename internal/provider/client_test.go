package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestClientCreateStatusListDestroy(t *testing.T) {
	c, state := fakeClient(t, "ok")
	ctx := context.Background()
	sb, err := c.Create(ctx, "fake-a1", map[string]string{"agent-deck/remote": "fake-a1", "agent-deck/profile": "work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sb.Host != "127.0.0.1" || sb.Port != 2222 || sb.User != "dev" || sb.Auth.Type != "key" {
		t.Fatalf("unexpected sandbox %+v", sb)
	}
	got, err := c.Status(ctx, sb.ID)
	if err != nil || got.ID != sb.ID {
		t.Fatalf("Status: %+v %v", got, err)
	}
	list, err := c.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != sb.ID {
		t.Fatalf("List: %+v %v", list, err)
	}
	if err := c.Destroy(ctx, sb.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if list, _ := c.List(ctx); len(list) != 0 {
		t.Fatalf("List after destroy: %+v", list)
	}

	reqs := readLines(t, filepath.Join(state, "requests.log"))
	want := []string{
		`{"protocol":1,"action":"create","name":"fake-a1","labels":{"agent-deck/profile":"work","agent-deck/remote":"fake-a1"}}`,
		`{"protocol":1,"action":"status","id":"` + sb.ID + `"}`,
		`{"protocol":1,"action":"list"}`,
		`{"protocol":1,"action":"destroy","id":"` + sb.ID + `"}`,
	}
	for i, w := range want {
		if i >= len(reqs) || reqs[i] != w {
			t.Fatalf("request %d = %q, want %q (all: %q)", i, at(reqs, i), w, reqs)
		}
	}
	for _, line := range readLines(t, filepath.Join(state, "env.log")) {
		if !strings.HasSuffix(line, "protocol=1 name=fake") {
			t.Fatalf("provider env missing protocol/name: %q", line)
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<missing>"
}

// The provider gets only the action in argv: names, ids, labels and
// anything secret travel on stdin or in its own env, never on a command line
// another user can read with ps. A SENTINEL placed in the provider's env and
// in the labels must not show in argv or in agent-deck's log output.
func TestClientArgvCarriesOnlyTheActionAndNoSecretReachesLogs(t *testing.T) {
	const sentinel = "SENTINEL-7f3a9c-do-not-leak"
	logDir := t.TempDir()
	logging.Init(logging.Config{Debug: true, LogDir: logDir, Level: "debug", Format: "json"})
	t.Cleanup(func() { logging.Shutdown(); logging.Init(logging.Config{}) })

	c, state := fakeClient(t, "ok")
	t.Setenv("FAKE_PROVIDER_TOKEN", sentinel)
	ctx := context.Background()
	sb, err := c.Create(ctx, "fake-s1", map[string]string{"agent-deck/remote": "fake-s1", "secret": sentinel})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Destroy(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	// A failing call logs too; its stderr must stay out of the log.
	t.Setenv("FAKE_PROVIDER_MODE", "stderr")
	_, _ = c.List(ctx)

	argv := readLines(t, filepath.Join(state, "argv.log"))
	wantArgv := []string{"argc=1 [create]", "argc=1 [status]", "argc=1 [list]", "argc=1 [destroy]", "argc=1 [list]"}
	if strings.Join(argv, "\n") != strings.Join(wantArgv, "\n") {
		t.Fatalf("argv = %q, want %q", argv, wantArgv)
	}
	if !strings.Contains(strings.Join(readLines(t, filepath.Join(state, "requests.log")), "\n"), sentinel) {
		t.Fatalf("sentinel label should travel on stdin (test precondition)")
	}
	logging.Shutdown()
	entries, _ := os.ReadDir(logDir)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("sentinel leaked into log %s", e.Name())
		}
		if strings.Contains(string(data), "quota exceeded") {
			t.Fatalf("provider stderr leaked into log %s", e.Name())
		}
	}
}

func TestClientProtocolErrors(t *testing.T) {
	tests := []struct {
		mode, wantErr string
	}{
		{"badjson", "decoding provider"},
		{"protocol2", "provider speaks protocol 2, agent-deck speaks 1"},
		{"error", "no capacity in region"},
		{"huge", "exceeds"},
		{"badhost", "host"},
		{"traversal", "key_path"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			c, _ := fakeClient(t, tt.mode)
			_, err := c.Create(context.Background(), "fake-x", nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Create error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeResponse(t *testing.T) {
	tests := []struct {
		name, in, wantErr string
	}{
		{"ok", `{"protocol":1,"ok":true}`, ""},
		{"no protocol", `{"ok":true}`, "protocol"},
		{"two objects", `{"protocol":1,"ok":true}{"protocol":1}`, "decoding provider"},
		{"not an object", `[1,2]`, "decoding provider"},
		{"empty", ``, "decoding provider"},
		{"bad expires_at", `{"protocol":1,"sandbox":{"id":"a","expires_at":"tomorrow"}}`, "decoding provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeResponse([]byte(tt.in))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// stderr is captured into the error text, with control characters removed.
func TestClientStderrInError(t *testing.T) {
	c, _ := fakeClient(t, "stderr")
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("expected failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "quota exceeded for project") {
		t.Fatalf("stderr missing from error: %q", msg)
	}
	if strings.ContainsRune(msg, 0x1b) {
		t.Fatalf("control characters not stripped: %q", msg)
	}
}

// A provider that outlives its timeout is killed with its whole process
// group (a child it started too), and the error says it timed out.
func TestClientTimeoutKillsProcessGroup(t *testing.T) {
	c, state := fakeClient(t, "slow")
	c.Config.TimeoutSeconds = 1
	start := time.Now()
	_, err := c.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	data, rerr := os.ReadFile(filepath.Join(state, "child.pid"))
	if rerr != nil {
		t.Fatalf("child pid not recorded: %v", rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("provider child %d survived the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processAlive treats a zombie (killed, not yet reaped) as dead.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

func TestClientCommandResolution(t *testing.T) {
	fakeEnv(t, "ok")
	for _, cmd := range []string{"", "relative/provider", "/nonexistent/agent-deck-provider"} {
		c := &Client{Name: "x", Config: session.ProviderConfig{Command: cmd}}
		if _, err := c.List(context.Background()); err == nil {
			t.Errorf("command %q accepted", cmd)
		}
	}
}

func TestLookupAndNames(t *testing.T) {
	cfg := &session.UserConfig{Providers: map[string]session.ProviderConfig{
		"zeta": {Command: "/bin/true"}, "alpha": {Command: "/bin/true"},
	}}
	if got := Names(cfg); strings.Join(got, ",") != "alpha,zeta" {
		t.Fatalf("Names = %q", got)
	}
	c, err := Lookup(cfg, "alpha")
	if err != nil || c.Name != "alpha" {
		t.Fatalf("Lookup: %+v %v", c, err)
	}
	if _, err := Lookup(cfg, "missing"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Lookup missing: %v", err)
	}
	pc := session.ProviderConfig{}
	if pc.GetCreateTimeout() != 600*time.Second || pc.GetTimeout() != 60*time.Second {
		t.Fatalf("defaults: %s %s", pc.GetCreateTimeout(), pc.GetTimeout())
	}
}
