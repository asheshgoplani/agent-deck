package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// An ntfy/slack watcher can carry an access token for a protected topic. Like
// the github HMAC secret it never comes from an inline CLI argument: it is read
// from $NTFY_TOKEN or a chmod-600 --token-file and stored in the 0600
// watcher.toml.

func TestResolveNtfyToken(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	if err := os.WriteFile(good, []byte("tk_from_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "NTFY_TOKEN" {
			return " tk_from_env "
		}
		return ""
	}

	if got, err := resolveNtfyToken("", env); err != nil || got != "tk_from_env" {
		t.Errorf("from env: got %q, %v", got, err)
	}
	if got, err := resolveNtfyToken(good, envNone); err != nil || got != "tk_from_file" {
		t.Errorf("from file: got %q, %v", got, err)
	}
	if got, err := resolveNtfyToken("", envNone); err != nil || got != "" {
		t.Errorf("no token configured: got %q, %v; a token is optional", got, err)
	}
}

func TestResolveNtfyToken_RejectsFile0644(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("tk_leaky"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveNtfyToken(path, envNone)
	want := "--token-file " + path + " has insecure permissions 0644; chmod 600 it"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// TestWatcherCreate_NtfyTokenAndServerReachEngine drives the real binary and
// checks the token and server land in the 0600 watcher.toml the engine reads,
// the token is never printed, and no guessable-topic warning is shown when a
// token protects the topic.
func TestWatcherCreate_NtfyTokenAndServerReachEngine(t *testing.T) {
	home := watcherCLIHome(t)
	const name = "ntfy-token-watcher"
	const token = "tk_must_not_be_printed"

	stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"NTFY_TOKEN=" + token},
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", name,
		"--topic", "alerts", "--server", "https://ntfy.example.com/")
	if code != 0 {
		t.Fatalf("create failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, token) {
		t.Errorf("the token was printed\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if strings.Contains(stderr, "easy to guess") {
		t.Errorf("warning shown for a token-protected topic: %s", stderr)
	}

	dir, err := session.WatcherNameDir(name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "watcher.toml")
	src := decodeWatcherSource(t, path)
	if src["token"] != token || src["server"] != "https://ntfy.example.com/" || src["topic"] != "alerts" {
		t.Errorf("[source] = %v, want topic, server and token persisted", src)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("watcher.toml mode = %#o, want 0600", perm)
	}
}

func TestWatcherCreate_RejectsWorldReadableTokenFile(t *testing.T) {
	home := watcherCLIHome(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("tk_leaky"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	const name = "ntfy-leaky-token"
	_, stderr, code := runAgentDeckEnv(t, home, "", []string{"NTFY_TOKEN="},
		"-p", watcherCLIProfile, "watcher", "create", "slack", "--name", name,
		"--topic", "alerts", "--token-file", path)
	if code == 0 {
		t.Fatal("create accepted a 0644 token file")
	}
	if !strings.Contains(stderr, "has insecure permissions 0644; chmod 600 it") {
		t.Errorf("stderr = %s", stderr)
	}
	if row := loadWatcherRow(t, name); row != nil {
		t.Errorf("watcher registered despite the refused token file")
	}
}

func TestWatcherCreate_WarnsForGuessablePublicTopic(t *testing.T) {
	home := watcherCLIHome(t)
	_, stderr, code := runAgentDeckEnv(t, home, "", []string{"NTFY_TOKEN="},
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", "guessable-topic", "--topic", "alerts")
	if code != 0 {
		t.Fatalf("create failed (exit %d): %s", code, stderr)
	}
	want := "Warning: " + watcher.PublicNtfyTopicWarning("ntfy", "", "alerts", "")
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr missing the warning\n got: %s\nwant: %s", stderr, want)
	}
}

func TestWatcherCreate_RandomTopic(t *testing.T) {
	home := watcherCLIHome(t)
	const name = "random-topic-watcher"
	stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"NTFY_TOKEN="},
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", name, "--random-topic")
	if code != 0 {
		t.Fatalf("create failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	dir, err := session.WatcherNameDir(name)
	if err != nil {
		t.Fatal(err)
	}
	topic := decodeWatcherSource(t, filepath.Join(dir, "watcher.toml"))["topic"]
	if topic == "" || watcher.GuessableNtfyTopic(topic) {
		t.Fatalf("generated topic %q is empty or guessable", topic)
	}
	if strings.Contains(stdout+stderr, topic) {
		t.Errorf("the generated topic was printed; it is a bearer capability\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if strings.Contains(stderr, "easy to guess") {
		t.Errorf("warning shown for a random topic: %s", stderr)
	}

	_, stderr, code = runAgentDeckEnv(t, home, "", nil,
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", "both-topics", "--topic", "x", "--random-topic")
	if code == 0 || !strings.Contains(stderr, "Error: --topic and --random-topic cannot be used together") {
		t.Errorf("--topic with --random-topic: exit %d, stderr %s", code, stderr)
	}
}
