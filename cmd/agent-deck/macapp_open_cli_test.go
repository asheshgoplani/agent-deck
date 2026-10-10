package main

// Black-box regressions for `agent-deck open` and `agent-deck file bundle`
// (the macOS app Browser panel contract). They drive only the built binary
// and the events package, so they compile on cores without the commands and
// fail there on behaviour, not on missing symbols.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

const macappOpenQueuedText = "Queued for AgentDeck: it opens when the app is connected"

func addOpenTestSession(t *testing.T, home, title string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "add", t.TempDir(), "--title", title, "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	return added.ID
}

type openTestStats struct {
	events.Stats
	Kinds map[string]uint64 `json:"kinds"`
}

func openTestBusDir(t *testing.T, home string) openTestStats {
	t.Helper()
	out, errOut, code := runAgentDeck(t, home, "events", "stats", "--json")
	var stats openTestStats
	if err := json.Unmarshal([]byte(out), &stats); err != nil || code != 0 || stats.Dir == "" {
		t.Fatalf("events stats: %d %s %s %v", code, out, errOut, err)
	}
	return stats
}

// A session (local or on a remote host, where SSH_CONNECTION is set) asks
// the app to open a report folder. The frame lands on that host's own bus
// with an absolute index.html path, and a correlated ack is reported.
func TestMacappOpenCLIDirectoryFrameAndAck(t *testing.T) {
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Report writer")
	stats := openTestBusDir(t, home)

	report := filepath.Join(t.TempDir(), "weekly report")
	if err := os.MkdirAll(report, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(report, "index.html")
	if err := os.WriteFile(page, []byte("<h1>ok</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}

	responder, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sub, err := responder.Subscribe(ctx, stats.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan events.Frame, 1)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		for frame := range sub.Frames() {
			if frame.Kind == "macapp.open" {
				got <- frame
				responder.Publish("macapp.open.ack", frame.SessionID, map[string]any{"open_event_id": "stale", "opened": true})
				responder.Publish("macapp.open.ack", frame.SessionID, map[string]any{"open_event_id": frame.EventID, "opened": true})
				return
			}
		}
	}()

	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "open", report)
	if code != 0 || strings.TrimSpace(stdout) != "Opened in AgentDeck (session Report writer)" {
		t.Fatalf("open: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var frame events.Frame
	select {
	case frame = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no macapp.open frame on the session's bus")
	}
	var data map[string]string
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if frame.SessionID != id || frame.EventID == "" || data["path"] != page || data["url"] != "" || data["host"] == "" {
		t.Fatalf("frame contract: %+v %s", frame, frame.Data)
	}
	if _, err := time.Parse(time.RFC3339Nano, data["requested_at"]); err != nil {
		t.Fatalf("requested_at: %v", err)
	}
}

// Without an app the request is still committed and the CLI says so; bad
// targets and missing sessions are refused and never queued.
func TestMacappOpenCLIQueuedAndRefusals(t *testing.T) {
	home := t.TempDir()
	id := addOpenTestSession(t, home, "Queue writer")
	env := []string{"AGENTDECK_INSTANCE_ID=" + id, "SSH_CONNECTION=fake-remote 1 2 3"}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "open", "https://example.com/report?q=a%20b")
	if code != 0 || strings.TrimSpace(stdout) != macappOpenQueuedText {
		t.Fatalf("queued: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stats := openTestBusDir(t, home)
	if stats.Kinds["macapp.open"] != 1 {
		t.Fatalf("queued request not committed: %+v", stats.Kinds)
	}

	marker := filepath.Join(t.TempDir(), "pwned")
	for _, target := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"https://user:secret@example.com/",
		"https://",
		"$(touch " + marker + ")",
		filepath.Join(t.TempDir(), "missing.html"),
		t.TempDir(), // a directory without index.html
	} {
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "open", target)
		if code == 0 || strings.Contains(stdout, "Queued") || strings.Contains(stdout, "Opened") {
			t.Errorf("target %q accepted: exit=%d stdout=%q stderr=%q", target, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target was interpreted by a shell: %v", err)
	}
	if got := openTestBusDir(t, home).Kinds["macapp.open"]; got != 1 {
		t.Fatalf("refused targets were queued: %d frames", got)
	}

	stdout, stderr, code = runAgentDeck(t, home, "open", "https://example.com")
	if code == 0 || !strings.Contains(stdout+stderr, "--session is required") {
		t.Fatalf("outside a session: exit=%d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", []string{"SSH_CONNECTION=x"}, "open", "https://example.com", "--session", "Queue writer")
	if code != 0 || strings.TrimSpace(stdout) != macappOpenQueuedText {
		t.Fatalf("--session by title: exit=%d %s %s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "open", "https://example.com", "--session", "nobody")
	if code == 0 || !strings.Contains(stdout+stderr, "not found") {
		t.Fatalf("unknown session: exit=%d %s %s", code, stdout, stderr)
	}
	// The app's acknowledgment must not need the generic plugin opt-in.
	writeMacappConfig(t, home, "[macapp]\nplugins = false\n")
	if out, errOut, code := runAgentDeck(t, home, "events", "publish", "--kind", "macapp.open.ack", "--session", id, "--data", `{"open_event_id":"x","opened":true}`); !builtinPublishAdmitted(code, out+errOut) {
		t.Fatalf("built-in ack gated: %d %s %s", code, out, errOut)
	}
}

// builtinPublishAdmitted reports whether `events publish` admitted a built-in
// kind past the plugin gate. Exit 0 is the normal case. The command's own
// commit budget (2 s, unchanged here) can expire on a saturated test host;
// that is exit 1 after admission, never the gate's exit 2.
func builtinPublishAdmitted(code int, output string) bool {
	if code == 0 {
		return true
	}
	return code == 1 && strings.Contains(output, "bus did not commit the frame") && !strings.Contains(output, "plugins = true")
}

func readBundleNames(t *testing.T, raw string) []string {
	t.Helper()
	reader := tar.NewReader(strings.NewReader(raw))
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "..") {
			t.Fatalf("unsafe archive name %q", header.Name)
		}
		names = append(names, header.Name)
	}
	sort.Strings(names)
	return names
}

// `file bundle` streams the page's folder as a tar so the app can cache a
// remote report with its relative assets. It is read-only and refuses
// symlinks and oversized folders without emitting a partial archive.
func TestFileBundleCLIStreamsPageFolder(t *testing.T) {
	home := t.TempDir()
	folder := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"report.html": "<link href=assets/site.css>", "assets/site.css": "body{}"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, code := runAgentDeck(t, home, "file", "bundle", filepath.Join(folder, "report.html"), "--session", "routing")
	if code != 0 {
		t.Fatalf("bundle: exit=%d stderr=%q", code, stderr)
	}
	if got, want := strings.Join(readBundleNames(t, stdout), ","), "assets/,assets/site.css,report.html"; got != want {
		t.Fatalf("archive entries %q, want %q", got, want)
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 2 {
		t.Fatalf("bundle wrote into the source folder: %v", entries)
	}

	stdout, stderr, code = runAgentDeck(t, home, "file", "bundle", folder)
	if code == 0 || stdout != "" {
		t.Fatalf("missing --session accepted: exit=%d stdout=%d bytes %s", code, len(stdout), stderr)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "assets", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "file", "bundle", folder, "--session", "routing")
	if code == 0 || stdout != "" || !strings.Contains(stderr, "symlink") {
		t.Fatalf("symlink escape: exit=%d stdout=%d bytes stderr=%q", code, len(stdout), stderr)
	}
	if err := os.Remove(filepath.Join(folder, "assets", "leak.txt")); err != nil {
		t.Fatal(err)
	}

	big, err := os.Create(filepath.Join(folder, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(21 << 20); err != nil {
		t.Fatal(err)
	}
	_ = big.Close()
	stdout, stderr, code = runAgentDeck(t, home, "file", "bundle", folder, "--session", "routing")
	if code == 0 || stdout != "" || !strings.Contains(stderr, "limit") {
		t.Fatalf("size cap: exit=%d stdout=%d bytes stderr=%q", code, len(stdout), stderr)
	}
}

// The app probes for both commands through their help text.
func TestMacappOpenAndFileBundleHelpAdvertised(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, _ := runAgentDeck(t, home, "open", "--help")
	if !strings.Contains(stdout+stderr, "agent-deck open <file|url> [--session <id|title>]") {
		t.Fatalf("open --help: %q %q", stdout, stderr)
	}
	stdout, stderr, code := runAgentDeck(t, home, "file", "bundle", "--help")
	if code != 0 || !strings.Contains(stdout+stderr, "agent-deck file bundle <dir|file> --session <id>") {
		t.Fatalf("file bundle --help: %d %q %q", code, stdout, stderr)
	}
}
