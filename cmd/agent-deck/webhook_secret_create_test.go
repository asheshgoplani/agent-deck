package main

import (
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// webhookTomlPath is where `watcher create webhook` stores a watcher's [source].
func webhookTomlPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := session.WatcherNameDir(name)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	return filepath.Join(dir, "watcher.toml")
}

// webhookStatuses starts an adapter from source on its port and returns the
// status each request (given as its headers) is answered with.
func webhookStatuses(t *testing.T, source map[string]string, requests ...map[string]string) []int {
	t.Helper()
	a := &watcher.WebhookAdapter{}
	if err := a.Setup(context.Background(), watcher.AdapterConfig{Type: "webhook", Name: "probe", Settings: source}); err != nil {
		t.Fatalf("Setup from the created [source]: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Listen(ctx, make(chan watcher.Event, len(requests))) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("webhook adapter did not stop")
		}
	}()

	url := "http://127.0.0.1:" + source["port"] + "/webhook"
	client := &http.Client{Timeout: 2 * time.Second}
	var codes []int
	for _, headers := range requests {
		var lastErr error
		code := 0
		for deadline := time.Now().Add(5 * time.Second); code == 0 && time.Now().Before(deadline); {
			req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("hello"))
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				time.Sleep(50 * time.Millisecond)
				continue
			}
			_ = resp.Body.Close()
			code = resp.StatusCode
		}
		if code == 0 {
			t.Fatalf("webhook on port %s never answered: %v", source["port"], lastErr)
		}
		codes = append(codes, code)
	}
	return codes
}

// A plain `watcher create webhook` generates a random secret, stores it at
// 0600 in watcher.toml [source], tells the user where it is and how to sign,
// and never prints the secret itself.
func TestWebhookCreate_GeneratesSecret0600(t *testing.T) {
	home := watcherCLIHome(t)
	const name = "hook-secret-gen"
	port := freeTCPPort(t)

	stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
		"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", name, "--port", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("create webhook failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	t.Logf("create output:\n%s", stdout)
	path := webhookTomlPath(t, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat watcher.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("watcher.toml mode = %#o, want 0600", perm)
	}
	source := decodeWatcherSource(t, path)
	secret := source["secret"]
	if raw, err := hex.DecodeString(secret); err != nil || len(raw) != 32 {
		t.Fatalf("[source].secret = %d chars, want a 32-byte hex secret", len(secret))
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Fatal("create printed the generated secret")
	}
	for _, want := range []string{path, "X-Webhook-Signature-256", "X-Webhook-Timestamp", "python3 - <<'EOF'", "curl "} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	// The examples keep the secret off command lines, where any local user
	// can read it in /proc/<pid>/cmdline.
	for _, leak := range []string{"-hmac", `"$SECRET"`, "X-Webhook-Token: $SECRET"} {
		if strings.Contains(stdout, leak) {
			t.Errorf("stdout puts the secret on a command line (%q):\n%s", leak, stdout)
		}
	}

	// The stored secret is the one the adapter enforces.
	codes := webhookStatuses(t, source, nil, map[string]string{"X-Webhook-Token": secret})
	if codes[0] != http.StatusUnauthorized {
		t.Errorf("unsigned request = %d, want 401", codes[0])
	}
	if codes[1] != http.StatusAccepted {
		t.Errorf("token request = %d, want 202", codes[1])
	}
}

// The secret can be supplied by $WEBHOOK_SECRET or a chmod-600
// --secret-file; an inline --secret and a readable-by-others file are refused.
func TestWebhookCreate_SecretSources(t *testing.T) {
	home := watcherCLIHome(t)

	t.Run("env", func(t *testing.T) {
		_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET=from-the-env-0123456789"},
			"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", "hook-env", "--port", strconv.Itoa(freeTCPPort(t)))
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if got := decodeWatcherSource(t, webhookTomlPath(t, "hook-env"))["secret"]; got != "from-the-env-0123456789" {
			t.Fatalf("[source].secret = %q, want the env value", got)
		}
	})

	t.Run("file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(f, []byte("  from-a-file-0123456789\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
			"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", "hook-file", "--port", strconv.Itoa(freeTCPPort(t)), "--secret-file", f)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if got := decodeWatcherSource(t, webhookTomlPath(t, "hook-file"))["secret"]; got != "from-a-file-0123456789" {
			t.Fatalf("[source].secret = %q, want the file value", got)
		}
	})

	t.Run("world-readable file refused", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(f, []byte("leaky"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f, 0o644); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
			"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", "hook-leaky", "--port", strconv.Itoa(freeTCPPort(t)), "--secret-file", f)
		if code == 0 || !strings.Contains(stderr, "chmod 600") {
			t.Fatalf("exit %d, stderr %q; want a refusal naming chmod 600", code, stderr)
		}
		if row := loadWatcherRow(t, "hook-leaky"); row != nil {
			t.Error("a refused create registered a watcher")
		}
	})

	weak := map[string]struct{ env, file, want string }{
		"blank env":  {env: "   ", want: "$WEBHOOK_SECRET is blank; use at least 16 bytes, e.g. from: openssl rand -hex 32"},
		"short env":  {env: "short-secret", want: "$WEBHOOK_SECRET is shorter than 16 bytes; use at least 16 bytes, e.g. from: openssl rand -hex 32"},
		"short file": {file: "short-secret\n", want: " is shorter than 16 bytes; use at least 16 bytes, e.g. from: openssl rand -hex 32"},
	}
	for name, c := range weak {
		t.Run(name+" refused", func(t *testing.T) {
			hook := "hook-weak-" + strings.ReplaceAll(name, " ", "-")
			args := []string{"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", hook, "--port", strconv.Itoa(freeTCPPort(t))}
			if c.file != "" {
				f := filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(f, []byte(c.file), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--secret-file", f)
				c.want = "--secret-file " + f + c.want
			}
			_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET=" + c.env}, args...)
			if code == 0 || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, stderr %q; want a refusal with %q", code, stderr, c.want)
			}
			if row := loadWatcherRow(t, hook); row != nil {
				t.Error("a refused create registered a watcher")
			}
		})
	}

	t.Run("inline secret refused", func(t *testing.T) {
		_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
			"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", "hook-inline", "--port", strconv.Itoa(freeTCPPort(t)), "--secret", "in-argv")
		if code == 0 || !strings.Contains(stderr, "--secret is insecure") {
			t.Fatalf("exit %d, stderr %q; want the inline --secret refusal", code, stderr)
		}
		if row := loadWatcherRow(t, "hook-inline"); row != nil {
			t.Error("a refused create registered a watcher")
		}
	})
}

// --no-secret keeps the old unsigned loopback-only behaviour for a user who
// asks for it, and stores no secret.
func TestWebhookCreate_NoSecret(t *testing.T) {
	home := watcherCLIHome(t)
	_, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
		"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", "hook-open", "--port", strconv.Itoa(freeTCPPort(t)), "--no-secret")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, ok := decodeWatcherSource(t, webhookTomlPath(t, "hook-open"))["secret"]; ok {
		t.Fatal("--no-secret stored a secret")
	}
}

// printedBlock returns the lines of out from the first one starting with
// first through the first one after it starting with last.
func printedBlock(t *testing.T, out, first, last string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, first) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], last) {
				return strings.Join(lines[i:j+1], "\n") + "\n"
			}
		}
	}
	t.Fatalf("output has no block from %q to %q:\n%s", first, last, out)
	return ""
}

// The signing and token examples `watcher create webhook` prints work as
// printed: run against the created watcher, each is accepted with its
// sender.
func TestWebhookCreate_PrintedExamplesVerify(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	home := watcherCLIHome(t)
	const name = "hook-examples"
	port := freeTCPPort(t)
	stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"WEBHOOK_SECRET="},
		"-p", watcherCLIProfile, "watcher", "create", "webhook", "--name", name, "--port", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("create webhook failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	signed := printedBlock(t, stdout, "python3 - <<'EOF'", "EOF")
	token := printedBlock(t, stdout, "  (umask 077;", "  curl ")

	source := decodeWatcherSource(t, webhookTomlPath(t, name))
	a := &watcher.WebhookAdapter{}
	if err := a.Setup(context.Background(), watcher.AdapterConfig{Type: "webhook", Name: name, Settings: source}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	events := make(chan watcher.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Listen(ctx, events) }()
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(5 * time.Second)
	for a.HealthCheck() != nil {
		if time.Now().After(deadline) {
			t.Fatal("webhook adapter never listened")
		}
		time.Sleep(20 * time.Millisecond)
	}

	run := func(script string) string {
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("example failed: %v\n%s\nscript:\n%s", err, out, script)
		}
		return strings.TrimSpace(string(out))
	}
	for label, script := range map[string]string{"signed": signed, "token": token} {
		if got := run(script); got != "202" {
			t.Fatalf("%s example printed %q, want 202\nscript:\n%s", label, got, script)
		}
		select {
		case evt := <-events:
			if evt.Sender != "my-service" {
				t.Fatalf("%s example: sender = %q, want my-service", label, evt.Sender)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s example produced no event", label)
		}
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(webhookTomlPath(t, name)), "token-header")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token header file: %v, want mode 0600", err)
	}
}
