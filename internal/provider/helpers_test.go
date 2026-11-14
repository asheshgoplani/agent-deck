package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeProviderPath is the absolute path of the fake provider script.
func fakeProviderPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "fake-provider.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeEnv points the fake provider at a fresh state dir and mode.
func fakeEnv(t *testing.T, mode string) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("FAKE_PROVIDER_STATE", state)
	t.Setenv("FAKE_PROVIDER_MODE", mode)
	t.Setenv("FAKE_PROVIDER_DESTROY", "ok")
	return state
}

func fakeClient(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	state := fakeEnv(t, mode)
	return &Client{Name: "fake", Config: session.ProviderConfig{Command: fakeProviderPath(t), TimeoutSeconds: 10, CreateTimeoutSeconds: 10}}, state
}

// freshHome gives the test its own HOME/XDG so config.toml and the sandbox
// dir start empty.
func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

// configureFake writes a config.toml with [providers.fake].
func configureFake(t *testing.T) {
	t.Helper()
	cfg := &session.UserConfig{Providers: map[string]session.ProviderConfig{
		"fake": {Command: fakeProviderPath(t), TimeoutSeconds: 10, CreateTimeoutSeconds: 10},
	}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// privateKey writes a 0600 key file and returns its path.
func privateKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
