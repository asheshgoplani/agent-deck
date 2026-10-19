package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
)

// Page text, titles, URLs and app messages are untrusted: no escape
// sequence (CSI, OSC, C1, BEL, DEL) reaches the agent's terminal, while
// line breaks and tabs of the page text stay.
func TestBrowserOutputDropsTerminalSequences(t *testing.T) {
	const evil = "a\x1b[2J\x1b]0;pwned\x07b\x9b31mc\x7fd\x1b]52;c;ZXZpbA==\x1b\\e\u202egnp.exe\u200bf\u2066g\ufeffh"
	check := func(name, got string) {
		t.Helper()
		for _, bad := range []string{"\x1b", "\x07", "\x9b", "\x7f", "\u202e", "\u200b", "\u2066", "\ufeff"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: %q passed through in %q", name, bad, got)
			}
		}
	}
	read, _ := json.Marshal(macapp.ReadResult{URL: "https://example.com/", Title: "T", Text: "line one\n\t" + evil})
	var out bytes.Buffer
	if err := writeBrowserResult(&out, macapp.OpRead, macapp.BrowserResponse{V: 1, OK: true, Result: read}, "", false); err != nil {
		t.Fatal(err)
	}
	check("read", out.String())
	if !strings.HasPrefix(out.String(), "line one\n ") {
		t.Errorf("read lost its line breaks: %q", out.String())
	}
	tabs, _ := json.Marshal(macapp.TabsResult{Tabs: []macapp.Tab{{TabID: "t1" + evil, URL: "https://example.com/" + evil, Title: evil, Active: true}}, Driver: evil})
	out.Reset()
	if err := writeBrowserResult(&out, macapp.OpTabs, macapp.BrowserResponse{V: 1, OK: true, Result: tabs}, "", false); err != nil {
		t.Fatal(err)
	}
	check("tabs", out.String())
	page, _ := json.Marshal(macapp.PageResult{TabID: "t1" + evil, URL: "https://example.com/" + evil})
	for _, op := range []macapp.Op{macapp.OpOpen, macapp.OpClick} {
		out.Reset()
		if err := writeBrowserResult(&out, op, macapp.BrowserResponse{V: 1, OK: true, Result: page}, "", false); err != nil {
			t.Fatal(err)
		}
		check(string(op), out.String())
	}
	check("error", browserResponseError(&macapp.ErrorInfo{Code: "bad_args" + evil, Message: evil}).Error())
}

// Large results stay out of the shared log: spilled to a 0600 file in a 0700
// directory, read back once (then deleted), checked against their digest,
// and swept after macapp.ResultTTL.
func TestBrowserResultSpill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("AGENTDECK_PROFILE", "")
	reqID := strings.Repeat("ab", 16)
	text := strings.Repeat("secret page text ", 2000)
	result, _ := json.Marshal(macapp.ReadResult{URL: "https://example.com/", Text: text})
	raw, _ := json.Marshal(macapp.BrowserResponse{V: 1, ReqID: reqID, RequestEventID: "e1", OK: true, Result: result})
	now := time.Now()

	spilled, err := spillBrowserResponse("", raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(spilled, []byte("secret page text")) || len(spilled) > 1024 {
		t.Fatalf("the result stayed in the frame: %d bytes", len(spilled))
	}
	var resp macapp.BrowserResponse
	if err := json.Unmarshal(spilled, &resp); err != nil || resp.ResultRef == nil || len(resp.Result) != 0 || resp.ReqID != reqID {
		t.Fatalf("spilled frame: %s %v", spilled, err)
	}
	dir, err := browserResultsDir("")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, resp.ResultRef.File)
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("side file: %v %v", info, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("side dir: %v %v", info, err)
	}
	back, err := loadBrowserResult("", resp.ResultRef)
	if err != nil || !bytes.Equal(back, result) {
		t.Fatalf("read back: %d bytes %v", len(back), err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("side file kept after it was read: %v", err)
	}

	// A tampered file is refused.
	spilled, _ = spillBrowserResponse("", raw, now)
	_ = json.Unmarshal(spilled, &resp)
	if err := os.WriteFile(filepath.Join(dir, resp.ResultRef.File), bytes.Repeat([]byte("x"), resp.ResultRef.Bytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBrowserResult("", resp.ResultRef); err == nil {
		t.Fatal("tampered result accepted")
	}
	for _, name := range []string{"../x.json", "x.json", reqID} {
		if _, err := loadBrowserResult("", &macapp.ResultRef{File: name}); err == nil {
			t.Fatalf("result_ref %q accepted", name)
		}
	}

	// A small result stays in the frame.
	small, _ := json.Marshal(macapp.BrowserResponse{V: 1, ReqID: reqID, OK: true, Result: json.RawMessage(`{"url":"https://example.com/"}`)})
	if got, err := spillBrowserResponse("", small, now); err != nil || !bytes.Equal(got, small) {
		t.Fatalf("small result: %s %v", got, err)
	}

	// Files past the TTL go on the next publish.
	spilled, _ = spillBrowserResponse("", raw, now)
	_ = json.Unmarshal(spilled, &resp)
	old := now.Add(-macapp.ResultTTL - time.Minute)
	if err := os.Chtimes(filepath.Join(dir, resp.ResultRef.File), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := spillBrowserResponse("", small, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, resp.ResultRef.File)); !os.IsNotExist(err) {
		t.Fatalf("expired side file kept: %v", err)
	}
}

// End to end: the app's reply goes through `events publish`, the page text
// never lands in the events log, and `agent-deck browser read` prints it.
func TestMacappBrowserCLIKeepsLargeResultsOutOfTheLog(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[browser]\nenabled = true\n")
	id := addOpenTestSession(t, home, "Big page")
	stats := openTestBusDir(t, home)
	reader, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub, err := reader.Subscribe(ctx, stats.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("private page line\n", 3000)
	go func() {
		for frame := range sub.Frames() {
			if frame.Kind != macapp.KindBrowserRequest {
				continue
			}
			var req macapp.BrowserRequest
			if json.Unmarshal(frame.Data, &req) != nil {
				continue
			}
			result, _ := json.Marshal(macapp.ReadResult{URL: "https://example.com/", Title: "Big", Text: text})
			reply, _ := json.Marshal(macapp.BrowserResponse{V: 1, ReqID: req.ReqID, RequestEventID: frame.EventID, OK: true, Result: result, Driver: macapp.DriverAgent})
			cmd := exec.Command(channelsCLIBinary(t), "events", "publish", "--kind", macapp.KindBrowserResponse, "--session", frame.SessionID, "--data-file", "-")
			cmd.Env = agentDeckTestEnv(home, nil)
			cmd.Stdin = bytes.NewReader(reply)
			_ = cmd.Run()
			return
		}
	}()
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "browser", "read", "--timeout", "20s")
	if code != 0 || stdout != text+"\n" {
		t.Fatalf("read: exit=%d %d bytes %q", code, len(stdout), stderr)
	}
	entries, err := os.ReadDir(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(stats.Dir, entry.Name()))
		if err == nil && bytes.Contains(raw, []byte("private page line")) {
			t.Fatalf("page text landed in the events log (%s)", entry.Name())
		}
	}
}

// A response in another protocol version is an error, not a result.
func TestBrowserResponseVersionChecked(t *testing.T) {
	parallelSubprocessTest(t)
	bus, responder := browserTestBuses(t)
	req, err := newBrowserRequest(macapp.OpTabs, browserFlags{}, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	startBrowserResponder(t, responder, bus.Stats().Cursor, func(macapp.BrowserRequest) *macapp.BrowserResponse {
		return &macapp.BrowserResponse{OK: true, Result: json.RawMessage(`{"tabs":[]}`)}
	})
	resp, err := publishBrowserRequest(bus, "s", req, 5*time.Second)
	if err != nil || resp.V != macapp.ProtocolVersion {
		t.Fatalf("v1 round trip: %+v %v", resp, err)
	}
	if !strings.Contains(checkBrowserResponseVersion(macapp.BrowserResponse{V: 2}).Error(), "protocol version 2") || checkBrowserResponseVersion(resp) != nil {
		t.Fatal("version check")
	}
}
