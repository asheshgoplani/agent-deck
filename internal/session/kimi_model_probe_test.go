package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const fakeKimiProvidersJSON = `{
  "providers": {
    "acme": {"type": "openai"},
    "zeta": {"type": "anthropic"}
  },
  "models": {
    "zeta/nova-3": {"provider": "zeta", "model": "nova-3"},
    "kimi-code/kimi-for-coding": {"provider": "kimi-code", "model": "kimi-for-coding"},
    "acme/nova-2": {"provider": "acme", "model": "nova-2"}
  }
}`

type fakeKimi struct {
	dir     string
	fixture string
	calls   string
	home    string
}

func installFakeKimi(t *testing.T, body string) *fakeKimi {
	t.Helper()
	dir := t.TempDir()
	f := &fakeKimi{
		dir:     dir,
		fixture: filepath.Join(dir, "providers.json"),
		calls:   filepath.Join(dir, "calls"),
		home:    filepath.Join(dir, "home"),
	}
	if err := os.WriteFile(f.fixture, []byte(fakeKimiProvidersJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writeStub(t, "kimi", body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("FAKE_KIMI_FIXTURE", f.fixture)
	t.Setenv("FAKE_KIMI_CALLS", f.calls)
	t.Setenv("FAKE_KIMI_HOME", f.home)
	t.Setenv(modelProbeTestEnv, "1")
	restoreCfg := resetUserConfigCache(t, &UserConfig{})
	ResetModelProbeMemo()
	t.Cleanup(func() {
		restoreCfg()
		ResetModelProbeMemo()
	})
	return f
}

func (f *fakeKimi) writeStub(t *testing.T, name, body string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'kimi-code 0.0.1'; exit 0; fi\n" +
		"echo \"$*\" >> \"$FAKE_KIMI_CALLS\"\n" +
		"printf '%s\\n' \"$KIMI_CODE_HOME\" > \"$FAKE_KIMI_HOME\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeKimi) probeCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

const fakeKimiOK = `cat "$FAKE_KIMI_FIXTURE"`

func TestKimiModelProbeSuccess(t *testing.T) {
	f := installFakeKimi(t, fakeKimiOK)

	want := []string{
		"acme/nova-2",
		"kimi-code/kimi-for-coding",
		"zeta/nova-3",
		"kimi-code/k3.1-pro",
		"kimi-code/k3.1-pro-highspeed",
		"kimi-code/k3",
		"kimi-code/k3-256k",
		"kimi-code/kimi-for-coding-highspeed",
	}
	if got := KnownModelIDsForTool("kimi"); !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("probe runs = %d, want 1", n)
	}
	data, err := os.ReadFile(f.calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "provider list --json" {
		t.Fatalf("probe command = %q, want provider list --json", got)
	}
}

func TestKimiModelProbeUsesConfiguredCommandAndHome(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		configDir    string
		wantKimiHome string
	}{
		{name: "config dir", command: "kimi-dev", configDir: "~/kimi config", wantKimiHome: "from config"},
		{name: "command prefix wins", command: "KIMI_CODE_HOME=/from-command kimi-dev", configDir: "~/kimi config", wantKimiHome: "/from-command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := installFakeKimi(t, fakeKimiOK)
			if err := os.Rename(filepath.Join(f.dir, "kimi"), filepath.Join(f.dir, "kimi-dev")); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			wantKimiHome := tt.wantKimiHome
			if wantKimiHome == "from config" {
				wantKimiHome = filepath.Join(home, "kimi config")
			}
			cfg := &UserConfig{}
			cfg.Kimi.Command = tt.command
			cfg.Kimi.ConfigDir = tt.configDir
			t.Cleanup(resetUserConfigCache(t, cfg))

			if got := KnownModelIDsForTool("kimi"); len(got) == 0 || got[0] != "acme/nova-2" {
				t.Fatalf("configured command was not probed: %v", got)
			}
			data, err := os.ReadFile(f.home)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(data)); got != wantKimiHome {
				t.Fatalf("KIMI_CODE_HOME = %q, want %q", got, wantKimiHome)
			}
		})
	}
}

func TestKimiModelProbeFailureFallsBack(t *testing.T) {
	cases := map[string]string{
		"non-zero exit":  `exit 1`,
		"malformed":      `echo 'not json'`,
		"missing models": `echo '{"providers":{}}'`,
		"empty models":   `echo '{"providers":{},"models":{}}'`,
		"invalid alias":  `echo '{"providers":{},"models":{"bad alias":{}}}'`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			installFakeKimi(t, body)
			want := []string{
				"kimi-code/k3.1-pro",
				"kimi-code/k3.1-pro-highspeed",
				"kimi-code/k3",
				"kimi-code/k3-256k",
				"kimi-code/kimi-for-coding",
				"kimi-code/kimi-for-coding-highspeed",
			}
			if got := KnownModelIDsForTool("kimi"); !slices.Equal(got, want) {
				t.Fatalf("models = %v, want static fallback %v", got, want)
			}
		})
	}
}

func TestKimiModelListOverride(t *testing.T) {
	f := installFakeKimi(t, fakeKimiOK)
	var cfg UserConfig
	if _, err := toml.Decode("[models.kimi]\nlist = ['acme/pinned']\n", &cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetUserConfigCache(t, &cfg))

	if got := KnownModelIDsForTool("kimi"); !slices.Equal(got, []string{"acme/pinned"}) {
		t.Fatalf("models = %v, want pinned list", got)
	}
	if n := f.probeCalls(t); n != 0 {
		t.Fatalf("pinned list ran the probe %d times", n)
	}
}
