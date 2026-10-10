package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
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
	if frame.SessionID != req.SessionID || data["session_id"] != "" || data["host"] == "" || data["url"] != req.URL || data["path"] != "" || len(data) != 3 || frame.EventID == "" {
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
	if frame.Kind != "macapp.open" || frame.SessionID != req.SessionID || frame.EventID == "" || len(data) != 3 || data["path"] != page || !filepath.IsAbs(data["path"]) || data["host"] == "" || data["requested_at"] == "" {
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
