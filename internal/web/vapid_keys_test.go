package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePushVAPIDKeysCreatesAndReuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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

	path := filepath.Join(home, ".agent-deck", "profiles", "test-profile", pushVAPIDKeysFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected vapid keys file to exist: %v", err)
	}
}
