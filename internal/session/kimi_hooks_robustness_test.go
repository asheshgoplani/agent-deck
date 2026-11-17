package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestKimiHooksMalformedConfigFailsWithoutMutation(t *testing.T) {
	for _, operation := range []string{"install", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.toml")
			original := []byte("this is not valid = [ TOML\n")
			if err := os.WriteFile(configPath, original, 0o600); err != nil {
				t.Fatal(err)
			}

			var err error
			if operation == "install" {
				_, err = session.InjectKimiHooks(dir)
			} else {
				_, err = session.RemoveKimiHooks(dir)
			}
			if err == nil {
				t.Fatal("malformed TOML unexpectedly succeeded")
			}
			got, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != string(original) {
				t.Fatalf("malformed config changed:\n got %q\nwant %q", got, original)
			}
			if session.CheckKimiHooksInstalled(dir) {
				t.Fatal("malformed config reported installed")
			}
		})
	}
}

func TestKimiHooksInlineArrayRejectedWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{name: "empty", config: "hooks = []\n"},
		{name: "nonempty", config: "hooks = [{ event = \"SessionStart\", command = \"agent-deck hook-handler\" }]\n"},
	} {
		for _, operation := range []string{"install", "uninstall"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				dir := t.TempDir()
				configPath := filepath.Join(dir, "config.toml")
				original := []byte(tc.config)
				if err := os.WriteFile(configPath, original, 0o600); err != nil {
					t.Fatal(err)
				}

				var err error
				if operation == "install" {
					_, err = session.InjectKimiHooks(dir)
				} else {
					_, err = session.RemoveKimiHooks(dir)
				}
				if err == nil || !strings.Contains(err.Error(), "inline array") {
					t.Fatalf("inline array error = %v, want clear rejection", err)
				}
				got, readErr := os.ReadFile(configPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != string(original) {
					t.Fatalf("rejected inline array changed config:\n got %q\nwant %q", got, original)
				}
			})
		}
	}
}

func TestKimiHooksPreserveSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	realPath := symlinkedFile(t, link, "default_model = \"kimi-code/k3\"\n")

	if _, err := session.InjectKimiHooks(dir); err != nil {
		t.Fatalf("InjectKimiHooks: %v", err)
	}
	assertStillSymlink(t, link)
	if !session.CheckKimiHooksInstalled(dir) {
		t.Fatal("hooks not installed through symlink")
	}
	removed, err := session.RemoveKimiHooks(dir)
	if err != nil || !removed {
		t.Fatalf("RemoveKimiHooks = %v, %v", removed, err)
	}
	assertStillSymlink(t, link)
	data, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "default_model") || strings.Contains(string(data), "agent-deck hook-handler") {
		t.Fatalf("symlink target after uninstall = %q", data)
	}
}

func TestKimiHooksConcurrentLifecyclePreservesUnrelatedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("default_model = \"sentinel-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := session.InjectKimiHooks(dir)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := session.RemoveKimiHooks(dir)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent lifecycle operation: %v", err)
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `default_model = "sentinel-model"`) {
		t.Fatalf("unrelated setting lost after concurrent lifecycle: %s", data)
	}
}

func TestKimiHooksNoOpUninstallAndFreshInstallFilesystemEffects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	removed, err := session.RemoveKimiHooks(dir)
	if err != nil || removed {
		t.Fatalf("no-op RemoveKimiHooks = %v, %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("no-op uninstall created config directory; stat err=%v", err)
	}

	installed, err := session.InjectKimiHooks(dir)
	if err != nil || !installed {
		t.Fatalf("fresh InjectKimiHooks = %v, %v", installed, err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("fresh config mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestCheckKimiHooksInstalledRequiresCompleteValidConfig(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if session.CheckKimiHooksInstalled(t.TempDir()) {
			t.Fatal("missing config reported installed")
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(configPath, []byte("[[hooks]]\nevent = \"SessionStart\"\ncommand = \"agent-deck hook-handler\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if session.CheckKimiHooksInstalled(dir) {
			t.Fatal("incomplete hook set reported installed")
		}
	})
	t.Run("non-array hooks", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.toml")
		original := []byte("[hooks]\nevent = \"SessionStart\"\n")
		if err := os.WriteFile(configPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := session.InjectKimiHooks(dir); err == nil {
			t.Fatal("non-array hooks unexpectedly accepted")
		}
		got, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("non-array config changed: %q", got)
		}
	})
}
