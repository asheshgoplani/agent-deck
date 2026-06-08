package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These regression tests pin the symlink-preserving write for the Hermes hook
// writers: a dotfiles-managed ~/.hermes/config.yaml that is a symlink must be
// updated through the link, leaving the symlink intact. See internal/atomicfile.

func TestInjectHermesHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "config.yaml")
	realPath := symlinkedFile(t, link, "{}\n")

	if _, err := session.InjectHermesHooks(configDir); err != nil {
		t.Fatalf("InjectHermesHooks: %v", err)
	}

	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hook-handler") {
		t.Fatalf("hooks not written through symlink to target; got: %s", data)
	}
}

func TestRemoveHermesHooks_PreservesSymlink(t *testing.T) {
	configDir := t.TempDir()
	link := filepath.Join(configDir, "config.yaml")
	symlinkedFile(t, link, "{}\n")

	if _, err := session.InjectHermesHooks(configDir); err != nil {
		t.Fatalf("InjectHermesHooks: %v", err)
	}
	if _, err := session.RemoveHermesHooks(configDir); err != nil {
		t.Fatalf("RemoveHermesHooks: %v", err)
	}

	assertStillSymlink(t, link)
}
