package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Security: GitHub webhook HMAC secret must not be a process-visible CLI flag
// (audit M2, sec-secrets-REPORT.md). CLI args leak via /proc/<pid>/cmdline and
// `ps auxww` and land in shell history. resolveGithubWebhookSecret sources the
// secret from the GITHUB_WEBHOOK_SECRET env var or a --secret-file (which must
// be chmod 600), and REFUSES an inline --secret.

func envNone(string) string { return "" }

// TestResolveGithubWebhookSecret_RejectsInlineFlag pins the core M2 guard: an
// inline secret value is refused with guidance toward the secure sources.
func TestResolveGithubWebhookSecret_RejectsInlineFlag(t *testing.T) {
	_, err := resolveGithubWebhookSecret("supersecret", "", envNone)
	if err == nil {
		t.Fatalf("expected inline --secret to be refused (M2), got nil error")
	}
	if !strings.Contains(err.Error(), "GITHUB_WEBHOOK_SECRET") && !strings.Contains(err.Error(), "secret-file") {
		t.Fatalf("refusal should point to the secure sources, got: %v", err)
	}
}

// TestResolveGithubWebhookSecret_FromEnv reads the secret from the env var.
func TestResolveGithubWebhookSecret_FromEnv(t *testing.T) {
	getenv := func(k string) string {
		if k == "GITHUB_WEBHOOK_SECRET" {
			return "  env-secret-value  "
		}
		return ""
	}
	got, err := resolveGithubWebhookSecret("", "", getenv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "env-secret-value" {
		t.Fatalf("expected trimmed env secret, got %q", got)
	}
}

// TestResolveGithubWebhookSecret_FromFile0600 reads from a chmod-600 file.
func TestResolveGithubWebhookSecret_FromFile0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	got, err := resolveGithubWebhookSecret("", path, envNone)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "file-secret" {
		t.Fatalf("expected trimmed file secret, got %q", got)
	}
}

// TestResolveGithubWebhookSecret_RejectsWorldReadableFile refuses a secret file
// with group/world permission bits set (not chmod 600).
func TestResolveGithubWebhookSecret_RejectsWorldReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("file-secret"), 0o644); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := resolveGithubWebhookSecret("", path, envNone)
	if err == nil {
		t.Fatalf("expected refusal for non-0600 secret file (M2), got nil error")
	}
	if !strings.Contains(err.Error(), "permission") && !strings.Contains(err.Error(), "600") {
		t.Fatalf("error should mention insecure permissions, got: %v", err)
	}
}

// TestResolveGithubWebhookSecret_RejectsEmptyFile refuses an empty secret file.
func TestResolveGithubWebhookSecret_RejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if _, err := resolveGithubWebhookSecret("", path, envNone); err == nil {
		t.Fatalf("expected refusal for empty secret file, got nil error")
	}
}

// TestResolveGithubWebhookSecret_RequiresASource errors when no secret is
// available from any secure source.
func TestResolveGithubWebhookSecret_RequiresASource(t *testing.T) {
	_, err := resolveGithubWebhookSecret("", "", envNone)
	if err == nil {
		t.Fatalf("expected error when no secret source is provided")
	}
}

// TestWriteGithubWatcherSecret_PersistsTo0600Toml proves the secret lands in the
// [source] table of watcher.toml at 0600 (the path the runtime engine reads),
// and that port is stored as a string so it decodes into the engine's
// map[string]string Settings.
func TestWriteGithubWatcherSecret_PersistsTo0600Toml(t *testing.T) {
	dir := t.TempDir()
	if err := writeGithubWatcherSecret(dir, "top-secret-hmac", 9000); err != nil {
		t.Fatalf("writeGithubWatcherSecret: %v", err)
	}

	path := filepath.Join(dir, "watcher.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat watcher.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("watcher.toml must be 0600, got %#o", perm)
	}

	var cfg struct {
		Source map[string]string `toml:"source"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decode watcher.toml: %v", err)
	}
	if cfg.Source["secret"] != "top-secret-hmac" {
		t.Fatalf("expected [source].secret persisted, got %q", cfg.Source["secret"])
	}
	if cfg.Source["port"] != "9000" {
		t.Fatalf("expected [source].port=\"9000\" (string), got %q", cfg.Source["port"])
	}
}
