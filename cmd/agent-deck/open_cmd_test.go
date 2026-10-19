package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
)

func TestOpenSessionIdentifier(t *testing.T) {
	for _, tc := range []struct {
		explicit, current, want string
		bad                     bool
	}{
		{"", "calling-id", "calling-id", false}, {"chosen", "calling-id", "chosen", false}, {"", "", "", true},
	} {
		got, err := openSessionIdentifier(tc.explicit, tc.current)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
}

func TestOpenTargetValidation(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	if err := os.WriteFile(page, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, page)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target, path, url string
		bad               bool
	}{
		{relative, page, "", false}, {dir, page, "", false}, {page, page, "", false},
		{"https://example.com/report?q=a%20b", "", "https://example.com/report?q=a%20b", false},
		{"http://localhost:8080/", "", "http://localhost:8080/", false},
		{filepath.Join(dir, "missing"), "", "", true}, {t.TempDir(), "", "", true},
		{"javascript:alert(1)", "", "", true}, {"file:///tmp/page", "", "", true},
		{"https://", "", "", true}, {"https://user:secret@example.com", "", "", true},
		{"https://example.com/\n", "", "", true}, {"https://[bad", "", "", true},
	} {
		restore := openCallerIsRemote
		openCallerIsRemote = func() bool { return false }
		defer func() { openCallerIsRemote = restore }()
		p, u, err := resolveOpenTarget(tc.target)
		if (err != nil) != tc.bad || (!tc.bad && (p != tc.path || u != tc.url)) {
			t.Errorf("%q: %q %q %v", tc.target, p, u, err)
		}
	}
}

func TestOpenEventAndAck(t *testing.T) {
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	responder, err := events.Open(bus.Stats().Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := responder.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	req, err := newOpenRequest("remote-session", "https://example.com/report")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan events.Frame, 1)
	go func() {
		for frame := range sub.Frames() {
			if frame.Kind == "macapp.open" {
				seen <- frame
				// A wrong request must not win even on the same session.
				responder.Publish("macapp.open.ack", req.SessionID, map[string]any{"open_event_id": "old", "opened": true})
				responder.Publish("macapp.open.ack", req.SessionID, map[string]any{"open_event_id": frame.EventID, "opened": true})
				return
			}
		}
	}()
	launched := false
	ack, err := publishOpenRequest(bus, req, 3*time.Second, func(MacappOpenRequest) { launched = true })
	if err != nil || !ack || launched {
		t.Fatalf("ack=%v launch=%v err=%v", ack, launched, err)
	}
	frame := <-seen
	var data map[string]string
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if frame.SessionID != req.SessionID || data["session_id"] != "" || data["host"] == "" || data["url"] != req.URL || data["path"] != "" || data["request_id"] != req.RequestID || req.RequestID == "" || len(data) != 4 || frame.EventID == "" {
		t.Fatalf("event: %s", frame.Data)
	}
	if _, err := time.Parse(time.RFC3339Nano, data["requested_at"]); err != nil {
		t.Fatal(err)
	}
	if got := openResultMessage("Report session", ack); got != "Opened in AgentDeck (session Report session)" {
		t.Fatal(got)
	}
}

func TestOpenQueuedAndNoFalseAck(t *testing.T) {
	parallelSubprocessTest(t)
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	req, err := newOpenRequest("s", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	launched := false
	ack, err := publishOpenRequest(bus, req, 1200*time.Millisecond, func(got MacappOpenRequest) { launched = got.EventID != ""; req.EventID = got.EventID })
	if err != nil || ack || !launched {
		t.Fatalf("ack=%v launch=%v err=%v", ack, launched, err)
	}
	if openResultMessage("s", ack) != openQueuedMessage {
		t.Fatal("wrong queued wording")
	}
	for _, frame := range []events.Frame{
		{Kind: "macapp.open.ack", SessionID: "other", Data: json.RawMessage(`{"open_event_id":"` + req.EventID + `","opened":true}`)},
		{Kind: "macapp.open.ack", SessionID: "s", Data: json.RawMessage(`{"open_event_id":"stale","opened":true}`)},
		{Kind: "macapp.open.ack", SessionID: "s", Data: json.RawMessage(`{}`)},
		{Kind: "macapp.open", SessionID: "s", Data: json.RawMessage(`{"open_event_id":"` + req.EventID + `","opened":true}`)},
	} {
		if matched, _ := openAcknowledged(frame, req); matched {
			t.Fatalf("false acknowledgment: %+v", frame)
		}
	}
	closed, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if ack, err := publishOpenRequest(closed, req, time.Second, nil); err == nil || ack {
		t.Fatal("closed bus claimed queued")
	}
}

func TestOpenLocalLaunchPolicy(t *testing.T) {
	for _, tc := range []struct {
		goos, ssh string
		want      bool
	}{{"darwin", "", true}, {"linux", "", false}, {"darwin", "SSH_CONNECTION", false}, {"darwin", "SSH_CLIENT", false}, {"darwin", "SSH_TTY", false}} {
		get := func(k string) string {
			if k == tc.ssh {
				return "set"
			}
			return ""
		}
		if localMacappLaunchAllowed(tc.goos, get) != tc.want {
			t.Fatalf("%+v", tc)
		}
	}
}

func TestOpenCLISelectionAndQueued(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	project := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "add", project, "--title", "Open report", "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	for _, tc := range []struct{ args, env []string }{
		{[]string{"open", "https://example.com"}, []string{"AGENTDECK_INSTANCE_ID=" + added.ID}},
		{[]string{"open", "https://example.com", "--session", "Open report"}, []string{"AGENTDECK_INSTANCE_ID=invalid"}},
	} {
		// SSH also ensures no GUI can be launched if this test runs on a Mac runner.
		env := append(tc.env, "SSH_CONNECTION=test")
		stdout, stderr, code = runAgentDeckEnv(t, home, "", env, tc.args...)
		if code != 0 || strings.TrimSpace(stdout) != openQueuedMessage {
			t.Fatalf("open: %d %s %s", code, stdout, stderr)
		}
	}
	// Read the selected bus location through the real CLI, then impersonate
	// a remote app follower. This catches publishing to default instead of
	// the session's inherited non-default profile.
	statsOut, statsErr, statsCode := runAgentDeck(t, home, "events", "stats", "--json")
	var stats events.Stats
	if err := json.Unmarshal([]byte(statsOut), &stats); err != nil || statsCode != 0 {
		t.Fatalf("stats: %s %s %v", statsOut, statsErr, err)
	}
	responder, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := responder.Subscribe(ctx, stats.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	responderDone := make(chan struct{})
	defer func() { cancel(); <-responderDone }()
	go func() {
		defer close(responderDone)
		for frame := range sub.Frames() {
			if frame.Kind == "macapp.open" {
				responder.Publish("macapp.open.ack", frame.SessionID, map[string]any{"open_event_id": frame.EventID, "opened": true})
				return
			}
		}
	}()
	stdout, stderr, code = runAgentDeckEnv(t, home, "", []string{"AGENTDECK_INSTANCE_ID=" + added.ID, "SSH_CONNECTION=fake-remote"}, "open", "https://example.com/ack")
	if code != 0 || strings.TrimSpace(stdout) != "Opened in AgentDeck (session Open report)" {
		t.Fatalf("CLI ack via inherited profile: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "open", "https://example.com")
	if code == 0 || !strings.Contains(stdout+stderr, "--session is required") {
		t.Fatalf("missing session: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "open", "https://example.com", "--session", "absent")
	if code == 0 || !strings.Contains(stdout+stderr, "not found") {
		t.Fatalf("unknown session: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "events", "publish", "--kind", "macapp.open.ack", "--session", added.ID, "--data", `{"open_event_id":"fake","opened":true}`)
	if !builtinPublishAdmitted(code, stdout+stderr) {
		t.Fatalf("builtin ack must not require plugins: %d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", []string{"AGENTDECK_EVENTS_BUS=0"}, "open", "https://example.com", "--session", added.ID)
	if code == 0 || strings.Contains(stdout, openQueuedMessage) {
		t.Fatalf("disabled bus: %d %s %s", code, stdout, stderr)
	}
}

func TestOpenAppRejectionIsNotSuccess(t *testing.T) {
	req := MacappOpenRequest{SessionID: "s", EventID: "e"}
	for _, data := range []string{
		`{"open_event_id":"e","opened":false,"error":"session not found"}`,
		`{"open_event_id":"e"}`,
		`{"open_event_id":"e","opened":true,"error":"failed"}`,
	} {
		matched, err := openAcknowledged(events.Frame{Kind: "macapp.open.ack", SessionID: "s", Data: json.RawMessage(data)}, req)
		if !matched || err == nil {
			t.Fatalf("rejection accepted: %s, %v", data, err)
		}
	}
	// Superseded request_id-only acks never count as the app's actual receipt.
	matched, err := openAcknowledged(events.Frame{Kind: "macapp.open.ack", SessionID: "s", Data: json.RawMessage(`{"request_id":"e","opened":true}`)}, req)
	if matched || err != nil {
		t.Fatalf("legacy ack matched: %v %v", matched, err)
	}
}

func TestOpenLinkMatchesPersistedEventID(t *testing.T) {
	parallelSubprocessTest(t)
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	folder := t.TempDir()
	page := filepath.Join(folder, "report & notes.html")
	if err := os.WriteFile(page, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	req, err := newOpenRequest("session & one", page)
	if err != nil {
		t.Fatal(err)
	}
	var link string
	ack, err := publishOpenRequest(bus, req, 1200*time.Millisecond, func(request MacappOpenRequest) { link = macappOpenLink("work profile", request) })
	if err != nil || ack || link == "" {
		t.Fatalf("queued: %v %v %s", ack, err, link)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	frame, ok := <-sub.Frames()
	if !ok {
		t.Fatal("request missing from event bus")
	}
	var data map[string]string
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if frame.Kind != "macapp.open" || frame.SessionID != req.SessionID || frame.EventID == "" || len(data) != 4 || data["request_id"] == "" || data["path"] != page || !filepath.IsAbs(data["path"]) || data["host"] == "" || data["requested_at"] == "" {
		t.Fatalf("app frame contract: %+v %s", frame, frame.Data)
	}
	if _, err := time.Parse(time.RFC3339, data["requested_at"]); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if parsed.Scheme != "agentdeck" || parsed.Host != "open" || q.Get("event") != frame.EventID || q.Get("session") != frame.SessionID || q.Get("path") != data["path"] || q.Get("profile") != "work profile" || q.Has("request_id") || q.Has("event_id") || q.Has("session_id") {
		t.Fatalf("app link contract: %s for %+v", link, frame)
	}
}

func TestOpenPublishBuiltinsWithoutPluginGate(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = false\n")
	for _, kind := range []string{"macapp.open", "macapp.open.ack"} {
		out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", kind, "--session", "s", "--data", `{"path":"/tmp/report.html"}`)
		if !builtinPublishAdmitted(code, out+stderr) {
			t.Fatalf("%s: %d %s %s", kind, code, out, stderr)
		}
	}
	for _, kind := range []string{"macapp.canvas.show", "macapp.open.other"} {
		out, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", kind)
		if code != 2 || !strings.Contains(out+stderr, "plugins = true") {
			t.Fatalf("plugin gate %s: %d %s %s", kind, code, out, stderr)
		}
	}
}

// T1: the open frame carries a fresh request_id (random hex) and the
// optional Browser panel profile as browser_profile, and the ack still
// matches on open_event_id alone.
func TestOpenFrameRequestIDProfileAndAck(t *testing.T) {
	parallelSubprocessTest(t)
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	responder, err := events.Open(bus.Stats().Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := responder.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	req, err := newOpenRequest("s", "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := newOpenRequest("s", "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestID == "" || req.RequestID == other.RequestID || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(req.RequestID) {
		t.Fatalf("request ids: %q %q", req.RequestID, other.RequestID)
	}
	req.BrowserProfile = "Research"
	seen := make(chan events.Frame, 1)
	go func() {
		for frame := range sub.Frames() {
			if frame.Kind == events.KindMacappOpen {
				seen <- frame
				// An ack naming only the request_id is not the app's receipt.
				responder.Publish(events.KindMacappOpenAck, req.SessionID, map[string]any{"request_id": req.RequestID, "opened": true})
				responder.Publish(events.KindMacappOpenAck, req.SessionID, map[string]any{"open_event_id": frame.EventID, "opened": true, "tab_id": "t1", "app_id": "mac-1"})
				return
			}
		}
	}()
	ack, err := publishOpenRequest(bus, req, 3*time.Second, nil)
	if err != nil || !ack {
		t.Fatalf("ack=%v err=%v", ack, err)
	}
	frame := <-seen
	var data map[string]string
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["request_id"] != req.RequestID || data["browser_profile"] != "Research" || data["profile"] != "" || data["url"] != "https://example.com/a" || len(data) != 5 {
		t.Fatalf("frame data: %s", frame.Data)
	}
}

// T1 (CLI): --browser-profile lands in the frame as browser_profile, and a
// bad --wait or --browser-profile is refused unqueued.
func TestOpenCLIProfileAndWaitFlags(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Profile writer")
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "open", "https://example.com/p", "--browser-profile", "Research", "--wait", "200ms")
	if code != 0 || strings.TrimSpace(stdout) != openQueuedMessage {
		t.Fatalf("open: exit=%d %q %q", code, stdout, stderr)
	}
	stats := openTestBusDir(t, home)
	if stats.Kinds[events.KindMacappOpen] != 1 {
		t.Fatalf("frames: %+v", stats.Kinds)
	}
	reader, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := reader.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]string
	for frame := range sub.Frames() {
		if frame.Kind == events.KindMacappOpen {
			if err := json.Unmarshal(frame.Data, &data); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if data["browser_profile"] != "Research" || data["request_id"] == "" {
		t.Fatalf("frame data: %v", data)
	}
	for _, args := range [][]string{
		{"open", "https://example.com/p", "--wait", "-1s"},
		{"open", "https://example.com/p", "--wait", "121s"},
		{"open", "https://example.com/p", "--browser-profile", "a\nb"},
		{"open", "https://example.com/p", "--browser-profile", "a\tb"},
		{"open", "https://example.com/p", "--browser-profile", "a\x1bb"},
		{"open", "https://example.com/p", "--browser-profile", "a\x7fb"},
		{"open", "https://example.com/p", "--browser-profile", "../../etc"},
		{"open", "https://example.com/p", "--browser-profile", ".hidden"},
		{"open", "https://example.com/p", "--browser-profile", "a b"},
		{"open", "https://example.com/p", "--browser-profile", strings.Repeat("a", 65)},
		{"open", "https://example.com/p", "--browser-profile", strings.Repeat("a", 100000)},
		// --profile is the global agent-deck profile, never the panel profile.
		{"open", "https://example.com/p", "--profile", "Research"},
	} {
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
		if code == 0 || strings.Contains(stdout, "Queued") {
			t.Fatalf("%q accepted: exit=%d %q %q", args, code, stdout, stderr)
		}
	}
	if !strings.Contains(func() string {
		_, e, _ := runAgentDeckEnv(t, home, "", env, "open", "https://example.com/p", "--wait", "121s")
		return e
	}(), "at most 2m0s") {
		t.Fatal("--wait over the cap must say the limit")
	}
	for _, ok := range []string{"Research", "work-2", "a.b_c", strings.Repeat("a", 64)} {
		if !validBrowserProfileName(ok) {
			t.Fatalf("%q refused", ok)
		}
	}
	if got := openTestBusDir(t, home).Kinds[events.KindMacappOpen]; got != 1 {
		t.Fatalf("refused requests were queued: %d", got)
	}
}

// T1: --wait bounds the acknowledgment wait, both in publishOpenRequest and
// through the CLI flag (no app answers here). Serial on purpose: it asserts
// wall-clock ceilings (2 s, openAckTimeout-500ms) that the package's parallel
// process-spawning tests would eat into on a loaded CI runner.
func TestOpenWaitBoundsTheAckWait(t *testing.T) {
	bus, err := events.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	req, err := newOpenRequest("s", "https://example.com/w")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ack, err := publishOpenRequest(bus, req, 300*time.Millisecond, nil)
	if elapsed := time.Since(start); err != nil || ack || elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("ack=%v err=%v after %s, want Queued after ~300ms", ack, err, elapsed)
	}

	home := t.TempDir()
	id := addOpenTestSession(t, home, "Wait bound")
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}
	for _, c := range []struct {
		wait     string
		min, max time.Duration
	}{
		{"0", 0, openAckTimeout - 500*time.Millisecond},
		{"1500ms", 1500 * time.Millisecond, openAckTimeout + 5*time.Second},
		{"5s", 5 * time.Second, 15 * time.Second},
	} {
		start := time.Now()
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "open", "https://example.com/w", "--wait", c.wait)
		elapsed := time.Since(start)
		if code != 0 || strings.TrimSpace(stdout) != openQueuedMessage || elapsed < c.min || elapsed > c.max {
			t.Fatalf("--wait %s: exit=%d %q %q after %s, want %s..%s", c.wait, code, stdout, stderr, elapsed, c.min, c.max)
		}
	}
}

// Defence in depth: on a host the app reaches as a remote, open and browser
// open refuse loopback, private and link-local addresses before writing a
// request; a local Mac session may still open its own dev server.
func TestOpenTargetRefusesInternalAddressesFromARemote(t *testing.T) {
	restore := openCallerIsRemote
	defer func() { openCallerIsRemote = restore }()
	openCallerIsRemote = func() bool { return true }
	for _, bad := range []string{"http://localhost:8080/", "http://192.168.1.1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/", "http://2130706433/",
		"http://１２７.０.０.１/", "http://ｌｏｃａｌｈｏｓｔ/", "http://127。0。0。1/", "http://100.100.1.1/", "http://router/"} {
		if _, _, err := resolveOpenTarget(bad); !errors.Is(err, macapp.ErrInternalAddress) {
			t.Errorf("%s from a remote: %v", bad, err)
		}
	}
	if _, u, err := resolveOpenTarget("https://example.com/"); err != nil || u != "https://example.com/" {
		t.Fatalf("public URL: %q %v", u, err)
	}
	openCallerIsRemote = func() bool { return false }
	if _, u, err := resolveOpenTarget("http://localhost:8080/"); err != nil || u != "http://localhost:8080/" {
		t.Fatalf("local Mac session: %q %v", u, err)
	}
	if !(func() bool { return !localMacappLaunchAllowed("linux", func(string) string { return "" }) }()) || localMacappLaunchAllowed("darwin", func(k string) string { return map[string]string{"SSH_CONNECTION": "x"}[k] }) {
		t.Fatal("a Linux host or an SSH session counts as a remote")
	}
}
