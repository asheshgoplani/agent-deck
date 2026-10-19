package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
)

// browserTestBuses opens a fresh bus for the CLI side and a second writer on
// the same log for the fake app.
func browserTestBuses(t *testing.T) (*events.Bus, *events.Bus) {
	t.Helper()
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	responder, err := events.Open(bus.Stats().Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = responder.Close() })
	return bus, responder
}

// startBrowserResponder is the fake Mac app: it follows the log after
// cursor and answers every browser request with answer's response (nil means
// stay silent). Around the real answer it publishes frames that must never
// match: another req_id, another request event, another session, another
// kind, and a later duplicate. It returns the request frames it saw.
func startBrowserResponder(t *testing.T, responder *events.Bus, after events.Cursor, answer func(macapp.BrowserRequest) *macapp.BrowserResponse) <-chan events.Frame {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := responder.Subscribe(ctx, after)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	seen := make(chan events.Frame, 32)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for frame := range sub.Frames() {
			if frame.Kind != macapp.KindBrowserRequest {
				continue
			}
			var req macapp.BrowserRequest
			if json.Unmarshal(frame.Data, &req) != nil {
				continue
			}
			select {
			case seen <- frame:
			default:
			}
			resp := answer(req)
			if resp == nil {
				continue
			}
			resp.V = macapp.ProtocolVersion
			resp.ReqID = req.ReqID
			resp.RequestEventID = frame.EventID
			decoy := macapp.BrowserResponse{V: macapp.ProtocolVersion, ReqID: req.ReqID, RequestEventID: frame.EventID, Error: &macapp.ErrorInfo{Code: macapp.CodeBadArgs, Message: "decoy"}}
			wrongReq := decoy
			wrongReq.ReqID = "other-request"
			wrongEvent := decoy
			wrongEvent.RequestEventID = "stale-event"
			responder.Publish(macapp.KindBrowserResponse, frame.SessionID, wrongReq)
			responder.Publish(macapp.KindBrowserResponse, frame.SessionID, wrongEvent)
			responder.Publish(macapp.KindBrowserResponse, "other-session", decoy)
			responder.Publish("macapp.browser.ack", frame.SessionID, decoy)
			responder.Publish(macapp.KindBrowserResponse, frame.SessionID, *resp)
			responder.Publish(macapp.KindBrowserResponse, frame.SessionID, decoy)
		}
	}()
	return seen
}

var browserTestImage = []byte{0xff, 0xd8, 0xff, 0xdb, 'j', 'p', 'e', 'g'}

// T3: one round trip per op through the fake app; only the matching first
// response counts, and each op prints its result.
func TestBrowserRoundTripPerOp(t *testing.T) {
	parallelSubprocessTest(t)
	bus, responder := browserTestBuses(t)
	cases := []struct {
		op     macapp.Op
		flags  browserFlags
		result any
		want   string
	}{
		{macapp.OpOpen, browserFlags{newTab: true}, macapp.PageResult{TabID: "t1", URL: "https://example.com/", Title: "Example"}, "Opened https://example.com/ (tab t1)\n"},
		{macapp.OpTabs, browserFlags{}, macapp.TabsResult{Tabs: []macapp.Tab{{TabID: "t1", URL: "https://example.com/", Title: "Example", Active: true}, {TabID: "t2", URL: "https://example.org/", Title: "Org"}}, Driver: macapp.DriverAgent}, "* t1\thttps://example.com/\tExample\n  t2\thttps://example.org/\tOrg\ndriver: agent\n"},
		{macapp.OpClose, browserFlags{tab: "t2"}, map[string]bool{"closed": true}, "Closed\n"},
		{macapp.OpClick, browserFlags{text: "Next"}, macapp.PageResult{URL: "https://example.com/next", Title: "Next"}, "https://example.com/next\n"},
		{macapp.OpType, browserFlags{ref: "e3", text: "hello", submit: true}, macapp.PageResult{URL: "https://example.com/next"}, "https://example.com/next\n"},
		{macapp.OpRead, browserFlags{}, macapp.ReadResult{URL: "https://example.com/", Title: "Example", Text: "Hello from the page"}, "Hello from the page\n"},
		{macapp.OpShot, browserFlags{full: true}, macapp.ShotResult{MIME: "image/jpeg", ImageB64: base64.StdEncoding.EncodeToString(browserTestImage), Width: 2, Height: 1}, ""},
	}
	results := map[macapp.Op]json.RawMessage{}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.result)
		if err != nil {
			t.Fatal(err)
		}
		results[tc.op] = raw
	}
	seen := startBrowserResponder(t, responder, 0, func(req macapp.BrowserRequest) *macapp.BrowserResponse {
		return &macapp.BrowserResponse{OK: true, Result: results[req.Op], Driver: macapp.DriverAgent, AppID: "test-app"}
	})
	for _, tc := range cases {
		req, err := newBrowserRequest(tc.op, tc.flags, "https://example.com/", 5*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		resp, err := publishBrowserRequest(bus, "box-session", req, 5*time.Second)
		if err != nil || !resp.OK || resp.ReqID != req.ReqID || resp.RequestEventID == "" || resp.AppID != "test-app" {
			t.Fatalf("%s: %+v %v", tc.op, resp, err)
		}
		frame := <-seen
		if frame.SessionID != "box-session" || frame.EventID != resp.RequestEventID {
			t.Fatalf("%s: request frame %+v", tc.op, frame)
		}
		shotPath := filepath.Join(t.TempDir(), "shots", "shot.jpg")
		var out bytes.Buffer
		if err := writeBrowserResult(&out, tc.op, resp, shotPath, false); err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		if tc.op == macapp.OpShot {
			got, err := os.ReadFile(shotPath)
			if err != nil || !bytes.Equal(got, browserTestImage) || out.String() != shotPath+"\n" {
				t.Fatalf("shot: %q %v %q", got, err, out.String())
			}
			continue
		}
		if out.String() != tc.want {
			t.Fatalf("%s output %q, want %q", tc.op, out.String(), tc.want)
		}
	}
}

// T2: the request frame carries exactly the contract fields, and the CLI's
// flags become the op's args.
func TestBrowserRequestFrameShape(t *testing.T) {
	parallelSubprocessTest(t)
	bus, _ := browserTestBuses(t)
	req, err := newBrowserRequest(macapp.OpType, browserFlags{selector: "#q", text: "hi", submit: true, tab: "t1"}, "", 120*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishBrowserRequest(bus, "s1", req, 50*time.Millisecond); !errors.Is(err, errBrowserNoAnswer) {
		t.Fatalf("no app: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	frame, ok := <-sub.Frames()
	if !ok || frame.Kind != "macapp.browser.request" || frame.SessionID != "s1" || frame.EventID == "" {
		t.Fatalf("frame: %+v", frame)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	// The bus re-encodes data, so compare args by value, not by key order.
	var args map[string]any
	if err := json.Unmarshal(data["args"], &args); err != nil {
		t.Fatal(err)
	}
	wantArgs := map[string]any{"tab_id": "t1", "selector": "#q", "text": "hi", "submit": true}
	if len(data) != 7 || string(data["v"]) != "1" || string(data["op"]) != `"type"` || !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("data: %s", frame.Data)
	}
	var got macapp.BrowserRequest
	if err := json.Unmarshal(frame.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Host == "" || len(got.ReqID) != 32 || got.ExpiresAt.Sub(got.RequestedAt) != 120*time.Second {
		t.Fatalf("envelope: %+v", got)
	}

	open, err := newBrowserRequest(macapp.OpOpen, browserFlags{browserProfile: "work"}, "https://example.com/a?b=c", time.Second)
	if err != nil || open.Args.URL != "https://example.com/a?b=c" || open.Args.BrowserProfile != "work" || open.ExpiresAt.Sub(open.RequestedAt) != browserOpenQueueTTL {
		t.Fatalf("open: %+v %v", open, err)
	}
	read, err := newBrowserRequest(macapp.OpRead, browserFlags{outline: true}, "", time.Second)
	if err != nil || read.Args.Mode != macapp.ReadModeOutline {
		t.Fatalf("read: %+v %v", read, err)
	}
	for _, tc := range []struct {
		op     macapp.Op
		flags  browserFlags
		target string
	}{
		{macapp.OpOpen, browserFlags{}, "javascript:alert(1)"},
		{macapp.OpOpen, browserFlags{}, "https://user:pw@example.com/"},
		{macapp.OpOpen, browserFlags{tab: "t1", newTab: true}, "https://example.com/"},
		{macapp.OpOpen, browserFlags{}, filepath.Join(t.TempDir(), "missing.html")},
		{macapp.OpClose, browserFlags{}, ""},
		{macapp.OpClick, browserFlags{}, ""},
		{macapp.OpClick, browserFlags{ref: "e1", selector: "#a"}, ""},
		{macapp.OpType, browserFlags{ref: "e1"}, ""},
		{macapp.OpType, browserFlags{ref: "e1", text: strings.Repeat("x", macapp.MaxTypeTextBytes+1)}, ""},
	} {
		if _, err := newBrowserRequest(tc.op, tc.flags, tc.target, time.Second); err == nil {
			t.Errorf("%s %+v %q accepted", tc.op, tc.flags, tc.target)
		}
	}
}

// T4: with no app the CLI waits its timeout and reports no answer; app
// error codes map to the documented exit codes.
func TestBrowserNoAnswerAndExitCodes(t *testing.T) {
	parallelSubprocessTest(t)
	bus, _ := browserTestBuses(t)
	req, err := newBrowserRequest(macapp.OpRead, browserFlags{}, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := publishBrowserRequest(bus, "s", req, 300*time.Millisecond); !errors.Is(err, errBrowserNoAnswer) {
		t.Fatalf("no answer: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("returned before the timeout: %s", elapsed)
	}
	closed, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, err := publishBrowserRequest(closed, "s", req, time.Second); err == nil || errors.Is(err, errBrowserNoAnswer) {
		t.Fatalf("closed bus: %v", err)
	}

	for _, tc := range []struct {
		info *macapp.ErrorInfo
		want int
	}{
		{nil, 1},
		{&macapp.ErrorInfo{Code: macapp.CodeNotConnected}, 3},
		{&macapp.ErrorInfo{Code: macapp.CodeOwnerDriving}, 4},
		{&macapp.ErrorInfo{Code: macapp.CodeNotYourTab}, 1},
		{&macapp.ErrorInfo{Code: macapp.CodeBadArgs}, 1},
		{&macapp.ErrorInfo{Code: macapp.CodeUnknownSession}, 1},
		{&macapp.ErrorInfo{Code: macapp.CodeRefusedURL}, 1},
		{&macapp.ErrorInfo{Code: "something_new"}, 1},
	} {
		if got := browserExitCode(tc.info); got != tc.want {
			t.Errorf("%+v: exit %d, want %d", tc.info, got, tc.want)
		}
	}
	if err := browserResponseError(&macapp.ErrorInfo{Code: macapp.CodeOwnerDriving, Message: "owner is typing"}); err == nil || err.Error() != "owner_driving: owner is typing" {
		t.Fatalf("error text: %v", err)
	}
	for op, want := range map[macapp.Op]time.Duration{
		macapp.OpOpen: 30 * time.Second, macapp.OpTabs: 30 * time.Second, macapp.OpRead: 30 * time.Second, macapp.OpShot: 30 * time.Second,
		macapp.OpClose: 30 * time.Second, macapp.OpClick: 120 * time.Second, macapp.OpType: 120 * time.Second,
	} {
		if got := browserDefaultTimeout(op); got != want {
			t.Errorf("%s timeout %s, want %s", op, got, want)
		}
	}
}

// Results over the caps are errors, and --json for a shot reports the file
// instead of repeating the image.
func TestBrowserResultCapsAndShotJSON(t *testing.T) {
	big, err := json.Marshal(macapp.ReadResult{Text: strings.Repeat("a", macapp.MaxReadTextBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeBrowserResult(&out, macapp.OpRead, macapp.BrowserResponse{OK: true, Result: big}, "", false); err == nil {
		t.Fatal("oversized read printed")
	}
	shot, err := json.Marshal(macapp.ShotResult{MIME: "image/jpeg", ImageB64: base64.StdEncoding.EncodeToString(browserTestImage), Width: 4, Height: 3})
	if err != nil {
		t.Fatal(err)
	}
	shotPath := filepath.Join(t.TempDir(), "s.jpg")
	out.Reset()
	if err := writeBrowserResult(&out, macapp.OpShot, macapp.BrowserResponse{V: 1, ReqID: "r", OK: true, Result: shot}, shotPath, true); err != nil {
		t.Fatal(err)
	}
	var printed macapp.BrowserResponse
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("json: %v %s", err, out.String())
	}
	var result map[string]any
	if err := json.Unmarshal(printed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["path"] != shotPath || result["width"] != float64(4) || result["image_b64"] != nil || !printed.OK {
		t.Fatalf("shot json: %s", out.String())
	}
	if got, err := os.ReadFile(shotPath); err != nil || !bytes.Equal(got, browserTestImage) {
		t.Fatalf("shot file: %v", err)
	}
}

// T5: --session and $AGENTDECK_INSTANCE_ID win; otherwise the calling tmux
// session decides; outside any session the command refuses.
func TestBrowserSessionResolution(t *testing.T) {
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Tmux caller")
	// Read the store the CLI just wrote, with the same profile and dirs the
	// subprocess used (agentDeckTestEnv).
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	const profile = "ch_support_test"
	instances, err := loadOpenSessions(profile)
	if err != nil {
		t.Fatal(err)
	}
	tmuxName := ""
	for _, inst := range instances {
		if inst.ID == id {
			tmuxName = inst.TmuxSession
		}
	}
	noTmux := func() string {
		t.Fatal("tmux consulted although a session was named")
		return ""
	}
	if got, err := resolveCallingSession(profile, "", id, noTmux); err != nil || got.ID != id {
		t.Fatalf("env id: %v %v", got, err)
	}
	if got, err := resolveCallingSession(profile, "Tmux caller", "stale-env-id", noTmux); err != nil || got.ID != id {
		t.Fatalf("--session title over env: %v %v", got, err)
	}
	if _, err := resolveCallingSession(profile, "nobody", "", noTmux); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown session: %v", err)
	}
	for _, name := range []string{"", "some-other-tmux-session"} {
		if _, err := resolveCallingSession(profile, "", "", func() string { return name }); err == nil || !strings.Contains(err.Error(), "--session is required") {
			t.Fatalf("tmux %q: %v", name, err)
		}
	}
	if tmuxName == "" {
		t.Skip("add recorded no tmux session name; tmux fallback covered only by refusal cases")
	}
	if got, err := resolveCallingSession(profile, "", "", func() string { return tmuxName }); err != nil || got.ID != id {
		t.Fatalf("tmux %q: %v %v", tmuxName, got, err)
	}
}

// T9: the browser command never falls back to the agentdeck:// URL scheme
// or launches anything, local or remote; the app reads the log.
func TestBrowserNeverLaunchesURLScheme(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "browser_cmd.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"agentdeck://", "launchLocalMacapp", "macappOpenLink", `"os/exec"`, "/usr/bin/open"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("browser_cmd.go uses %s", banned)
		}
	}
}
