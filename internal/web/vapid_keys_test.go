package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestEnsurePushVAPIDKeysCreatesAndReuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Clear XDG so resolution tracks this test's HOME (post-#1294 a fresh
	// profile data dir resolves under XDG; assert the resolved path rather
	// than a hardcoded legacy ~/.agent-deck location).
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	pub1, priv1, generated1, err := EnsurePushVAPIDKeys("test-profile", "mailto:test@example.com")
	if err != nil {
		t.Fatalf("EnsurePushVAPIDKeys first call failed: %v", err)
	}
	if !generated1 {
		t.Fatalf("expected first call to generate keys")
	}
	if pub1 == "" || priv1 == "" {
		t.Fatalf("expected generated keys to be non-empty")
	}

	pub2, priv2, generated2, err := EnsurePushVAPIDKeys("test-profile", "mailto:test@example.com")
	if err != nil {
		t.Fatalf("EnsurePushVAPIDKeys second call failed: %v", err)
	}
	if generated2 {
		t.Fatalf("expected second call to reuse existing keys")
	}
	if pub1 != pub2 || priv1 != priv2 {
		t.Fatalf("expected persisted keys to be reused")
	}

	profileDir, err := session.GetProfileDir("test-profile")
	if err != nil {
		t.Fatalf("resolve profile dir: %v", err)
	}
	path := filepath.Join(profileDir, pushVAPIDKeysFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected vapid keys file to exist: %v", err)
	}
}
