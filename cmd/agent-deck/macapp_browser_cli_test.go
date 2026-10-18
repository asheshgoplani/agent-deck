package main

// Black-box regressions for `agent-deck browser <op>`: the built binary
// against a fake app that follows the same events log (the pattern of
// macapp_open_cli_test.go).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
)

// T3/T4 through the binary: read and shot round trips, app refusals mapped
// to exit codes, and no answer within --timeout.
func TestMacappBrowserCLIRoundTripAndExitCodes(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[browser]\nenabled = true\n")
	id := addOpenTestSession(t, home, "Browser driver")
	stats := openTestBusDir(t, home)
	responder, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = responder.Close() })
	image := base64.StdEncoding.EncodeToString(browserTestImage)
	seen := startBrowserResponder(t, responder, stats.Cursor, func(req macapp.BrowserRequest) *macapp.BrowserResponse {
		switch req.Op {
		case macapp.OpRead:
			return &macapp.BrowserResponse{OK: true, Driver: macapp.DriverAgent, Result: json.RawMessage(`{"url":"https://example.com/","title":"Example","text":"Page text"}`)}
		case macapp.OpShot:
			return &macapp.BrowserResponse{OK: true, Driver: macapp.DriverAgent, Result: json.RawMessage(`{"mime":"image/jpeg","image_b64":"` + image + `","width":2,"height":1}`)}
		case macapp.OpClick:
			return &macapp.BrowserResponse{Driver: macapp.DriverOwner, Error: &macapp.ErrorInfo{Code: macapp.CodeOwnerDriving, Message: "the owner is driving"}}
		case macapp.OpClose:
			return &macapp.BrowserResponse{Error: &macapp.ErrorInfo{Code: macapp.CodeNotYourTab, Message: "tab t9 belongs to another session"}}
		}
		return nil // tabs and open: the app is not connected
	})
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "browser", "read")
	if code != 0 || stdout != "Page text\n" {
		t.Fatalf("read: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	frame := <-seen
	var req macapp.BrowserRequest
	if err := json.Unmarshal(frame.Data, &req); err != nil {
		t.Fatal(err)
	}
	if frame.SessionID != id || req.Op != macapp.OpRead || req.Args.Mode != macapp.ReadModeText || req.Host == "" || req.ExpiresAt.Sub(req.RequestedAt) != 30*time.Second {
		t.Fatalf("read frame: %+v %s", frame, frame.Data)
	}

	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "read", "--json")
	var resp macapp.BrowserResponse
	if code != 0 || json.Unmarshal([]byte(stdout), &resp) != nil || !resp.OK || resp.ReqID == "" || resp.RequestEventID == "" {
		t.Fatalf("read --json: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	out := filepath.Join(t.TempDir(), "page.jpg")
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "shot", "--out", out)
	if got, err := os.ReadFile(out); code != 0 || strings.TrimSpace(stdout) != out || err != nil || !bytes.Equal(got, browserTestImage) {
		t.Fatalf("shot --out: exit=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "shot", "--full")
	path := strings.TrimSpace(stdout)
	if got, err := os.ReadFile(path); code != 0 || err != nil || !bytes.Equal(got, browserTestImage) || !strings.HasPrefix(path, home) || !strings.Contains(path, filepath.Join("runtime", "browser-shots", id)+string(filepath.Separator)) {
		t.Fatalf("shot default path: exit=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}

	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "click", "--ref", "e4")
	if code != 4 || !strings.Contains(stderr, "owner_driving") {
		t.Fatalf("owner driving: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "close", "--tab", "t9")
	if code != 1 || !strings.Contains(stderr, "not_your_tab") {
		t.Fatalf("not your tab: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "tabs", "--timeout", "1s")
	if code != 3 || !strings.Contains(stderr, "not_connected") {
		t.Fatalf("no answer: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "browser", "open", "https://example.com/report", "--timeout", "1s")
	if code != 0 || strings.TrimSpace(stdout) != macappOpenQueuedText {
		t.Fatalf("open queued: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// T5 and T2 through the binary: session selection, refused arguments and
// unknown sessions never reach the log; help is advertised.
func TestMacappBrowserCLISessionAndRefusals(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[browser]\nenabled = true\n")
	id := addOpenTestSession(t, home, "Browser refusals")
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}

	for _, args := range [][]string{
		{"browser"},
		{"browser", "eval"},
		{"browser", "open"},
		{"browser", "open", "javascript:alert(1)"},
		{"browser", "open", "https://user:secret@example.com/"},
		{"browser", "open", "https://example.com", "--tab", "t1", "--new-tab"},
		{"browser", "tabs", "--tab", "t1"},
		{"browser", "tabs", "extra"},
		{"browser", "close"},
		{"browser", "click"},
		{"browser", "click", "--ref", "e1", "--selector", "#a"},
		{"browser", "type", "--ref", "e1"},
		{"browser", "read", "--submit"},
		{"browser", "shot", "--timeout", "-1s"},
	} {
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
		if code != 1 {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	stdout, stderr, code := runAgentDeck(t, home, "browser", "tabs", "--timeout", "1s")
	if code != 1 || !strings.Contains(stdout+stderr, "--session is required") {
		t.Fatalf("outside a session: exit=%d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "browser", "tabs", "--session", "nobody", "--timeout", "1s")
	if code != 1 || !strings.Contains(stdout+stderr, "not found") {
		t.Fatalf("unknown session: exit=%d %s %s", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds["macapp.browser.request"]; got != 0 {
		t.Fatalf("refused requests reached the log: %d frames", got)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", []string{"SSH_CONNECTION=x"}, "browser", "open", "https://example.com", "--session", "Browser refusals", "--timeout", "1s")
	if code != 0 || strings.TrimSpace(stdout) != macappOpenQueuedText {
		t.Fatalf("--session by title: exit=%d %s %s", code, stdout, stderr)
	}
	if got := openTestBusDir(t, home).Kinds["macapp.browser.request"]; got != 1 {
		t.Fatalf("queued open not committed: %d frames", got)
	}

	for _, args := range [][]string{{"browser", "--help"}, {"browser", "help"}, {"browser", "open", "--help"}, {"browser", "shot", "-h"}} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 || !strings.Contains(stdout, "Usage: agent-deck browser <op>") {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

// A browser request needs the [macapp] plugins opt-in. It is an opt-in, not
// a security boundary (an agent on the host controls its config and log; the
// Mac app enforces the policy). Without it nothing reaches the log.
func TestMacappBrowserCLINeedsPluginsOptIn(t *testing.T) {
	for _, cfg := range []string{"[browser]\nenabled = true\n", "[macapp]\nplugins = false\n\n[browser]\nenabled = true\n"} {
		home := t.TempDir()
		writeMacappConfig(t, home, cfg)
		id := addOpenTestSession(t, home, "Browser gated")
		for _, args := range [][]string{
			{"browser", "tabs", "--session", id, "--timeout", "1s"},
			{"browser", "open", "https://example.com", "--session", id, "--timeout", "1s"},
		} {
			stdout, stderr, code := runAgentDeck(t, home, args...)
			if code != 1 || !strings.Contains(stdout+stderr, "plugins = true") {
				t.Fatalf("%q with %q: exit=%d %s %s", args, cfg, code, stdout, stderr)
			}
		}
		if got := openTestBusDir(t, home).Kinds["macapp.browser.request"]; got != 0 {
			t.Fatalf("a gated request reached the log: %d frames", got)
		}
	}
}
