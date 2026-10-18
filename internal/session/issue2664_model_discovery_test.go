package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// Issue #2664: OpenCode's catalog comes from `opencode models`, and
// [models.<tool>] pins or extends any tool's list. Each case installs a fake
// `opencode` shell stub on PATH; nothing here runs a real CLI.

const fakeOpenCodeModels = `opencode/big-pickle
acme/orbit-3
acme/nova-2
router/acme/orbit-2
`

type fakeOpenCode struct {
	dir   string
	calls string
}

// installFakeOpenCode puts an `opencode` stub first on PATH, points the probe
// cache at a temp dir and opts this test binary into probing.
func installFakeOpenCode(t *testing.T, body string) *fakeOpenCode {
	t.Helper()
	dir := t.TempDir()
	f := &fakeOpenCode{dir: dir, calls: filepath.Join(dir, "calls")}
	fixture := filepath.Join(dir, "models.txt")
	if err := os.WriteFile(fixture, []byte(fakeOpenCodeModels), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writeStub(t, "opencode", body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("FAKE_OPENCODE_FIXTURE", fixture)
	t.Setenv("FAKE_OPENCODE_CALLS", f.calls)
	t.Setenv(modelProbeTestEnv, "1")
	restoreCfg := resetUserConfigCache(t, &UserConfig{})
	ResetModelProbeMemo()
	t.Cleanup(func() {
		restoreCfg()
		ResetModelProbeMemo()
	})
	return f
}

func (f *fakeOpenCode) writeStub(t *testing.T, name, body string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '1.19.3'; exit 0; fi\n" +
		"echo \"$*\" >> \"$FAKE_OPENCODE_CALLS\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeOpenCode) probeCalls(t *testing.T) int {
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

const fakeOpenCodeOK = `cat "$FAKE_OPENCODE_FIXTURE"`

func setModelsConfig(t *testing.T, models ModelsSettings) {
	t.Helper()
	cfg := &UserConfig{}
	cfg.Models = models
	t.Cleanup(resetUserConfigCache(t, cfg))
}

func TestIssue2664_OpenCodeProbeMergesFirst(t *testing.T) {
	f := installFakeOpenCode(t, fakeOpenCodeOK)

	want := []string{"opencode/big-pickle", "acme/orbit-3", "acme/nova-2", "router/acme/orbit-2"}
	for _, tool := range []string{"opencode", "opencode2"} {
		models := KnownModelIDsForTool(tool)
		if len(models) < len(want) || !slices.Equal(models[:len(want)], want) {
			t.Fatalf("%s: got %v, want probed prefix %v", tool, models, want)
		}
		for _, id := range staticModelIDsForTool("opencode") {
			if !slices.Contains(models, id) {
				t.Fatalf("%s: static id %q dropped; the static catalog is the floor", tool, id)
			}
		}
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("%d probe runs, want 1 (opencode2 shares the opencode probe)", n)
	}
	// OpenCode has no reasoning-effort override; the probe must not add one.
	if got := LaunchReasoningEffortsForTool("opencode"); len(got) != 0 {
		t.Fatalf("opencode efforts = %v, want none", got)
	}
}

// The probe runs from the home directory, so a project opencode.json in the
// caller's cwd cannot change the cached, per-binary answer.
func TestIssue2664_OpenCodeProbeRunsFromHome(t *testing.T) {
	f := installFakeOpenCode(t, `pwd > "$FAKE_OPENCODE_CALLS.pwd"; cat "$FAKE_OPENCODE_FIXTURE"`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	t.Chdir(project)

	KnownModelIDsForTool("opencode")
	data, err := os.ReadFile(f.calls + ".pwd")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	wantDir, _ := filepath.EvalSymlinks(home)
	if got != wantDir {
		t.Fatalf("probe ran in %q, want home %q", got, wantDir)
	}
}

func TestIssue2664_OpenCodeProbeFailureFallsBack(t *testing.T) {
	cases := map[string]string{
		"non-zero exit":  `echo 'Error: boom' >&2; exit 1`,
		"empty output":   `exit 0`,
		"header line":    `echo 'Available models:'; cat "$FAKE_OPENCODE_FIXTURE"`,
		"verbose json":   `echo 'acme/nova-1'; echo '{'; echo '}'`,
		"all ids unsafe": `echo 'openai/rm -rf'`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			installFakeOpenCode(t, body)
			if got := KnownModelIDsForTool("opencode"); !slices.Equal(got, staticModelIDsForTool("opencode")) {
				t.Fatalf("got %v, want the static catalog", got)
			}
			assertFailureCachedBriefly(t, "opencode")
		})
	}
}

// OpenCode boots its runtime before listing, so it gets more than the 1s
// Codex timeout; a stuck CLI is still cut off.
func TestIssue2664_OpenCodeProbeTimeout(t *testing.T) {
	prev := modelProbeTimeout
	modelProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { modelProbeTimeout = prev })

	installFakeOpenCode(t, `sleep 1; cat "$FAKE_OPENCODE_FIXTURE"`)
	if got := KnownModelIDsForTool("opencode"); got[0] != "opencode/big-pickle" {
		t.Fatalf("a 1s start must fit the opencode timeout, got %v", got)
	}

	installFakeOpenCode(t, `sleep 10; cat "$FAKE_OPENCODE_FIXTURE"`)
	start := time.Now()
	got := KnownModelIDsForTool("opencode")
	if elapsed := time.Since(start); elapsed > openCodeModelProber.timeout+2*time.Second {
		t.Fatalf("probe blocked %s despite the timeout", elapsed)
	}
	if !slices.Equal(got, staticModelIDsForTool("opencode")) {
		t.Fatalf("got %v, want the static catalog", got)
	}
}

func TestIssue2664_OpenCodeUsesConfiguredCommand(t *testing.T) {
	f := installFakeOpenCode(t, fakeOpenCodeOK)
	if err := os.Rename(filepath.Join(f.dir, "opencode"), filepath.Join(f.dir, "opencode-dev")); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{}
	cfg.OpenCode.Command = "OPENCODE_CONFIG=/tmp/oc.json opencode-dev --print-logs"
	t.Cleanup(resetUserConfigCache(t, cfg))

	if got := openCodeProbeBinary(); got != "opencode-dev" {
		t.Fatalf("openCodeProbeBinary() = %q, want opencode-dev", got)
	}
	if got := KnownModelIDsForTool("opencode"); got[0] != "opencode/big-pickle" {
		t.Fatalf("probe did not use the configured command: %v", got)
	}
}

func TestParseOpenCodeModels(t *testing.T) {
	got, err := parseOpenCodeModels([]byte("a/x\r\n\nopenrouter/b/y\na/x\ncloudflare/@cf/odd\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Models, []string{"a/x", "openrouter/b/y"}) {
		t.Fatalf("models = %v (want CLI order, nested slashes kept, dupes and unsafe ids dropped)", got.Models)
	}
	for name, input := range map[string]string{
		"no slash":       "a/x\nheader\n",
		"empty provider": "/x\n",
		"empty model":    "a/\n",
		"nothing":        "\n\n",
	} {
		if _, err := parseOpenCodeModels([]byte(input)); err == nil {
			t.Errorf("%s: parser accepted %q", name, input)
		}
	}
}

// [models.<tool>] list pins the suggestions: no probe, no built-in catalog.
func TestIssue2664_ConfigListPins(t *testing.T) {
	f := installFakeOpenCode(t, fakeOpenCodeOK)
	setModelsConfig(t, ModelsSettings{
		OpenCode: ModelListSettings{List: []string{" acme/nova-2 ", "", "local/qwen", "local/qwen"}},
		Codex:    ModelListSettings{List: []string{"nova-2"}},
	})
	if got := KnownModelIDsForTool("opencode"); !slices.Equal(got, []string{"acme/nova-2", "local/qwen"}) {
		t.Fatalf("opencode = %v, want the pinned list only", got)
	}
	if got := KnownModelIDsForTool("codex"); !slices.Equal(got, []string{"nova-2"}) {
		t.Fatalf("codex = %v, want the pinned list only", got)
	}
	if n := f.probeCalls(t); n != 0 {
		t.Fatalf("a pinned list must not run the CLI, ran %d times", n)
	}
	// Other tools are untouched.
	if got := KnownModelIDsForTool("gemini"); !slices.Equal(got, staticModelIDsForTool("gemini")) {
		t.Fatalf("gemini = %v", got)
	}
}

// [models.<tool>] extra comes first, then the probe, then the built-ins.
func TestIssue2664_ConfigExtraLeads(t *testing.T) {
	installFakeOpenCode(t, fakeOpenCodeOK)
	setModelsConfig(t, ModelsSettings{
		OpenCode: ModelListSettings{Extra: []string{"local/qwen", "acme/nova-2"}},
		Claude:   ModelListSettings{Extra: []string{"orbit-custom"}},
	})
	got := KnownModelIDsForTool("opencode")
	want := []string{"local/qwen", "acme/nova-2", "opencode/big-pickle", "acme/orbit-3", "router/acme/orbit-2"}
	if !slices.Equal(got[:len(want)], want) {
		t.Fatalf("opencode = %v, want prefix %v", got, want)
	}
	if !slices.Contains(got, staticModelIDsForTool("opencode")[0]) {
		t.Fatalf("extra must not drop the built-in catalog: %v", got)
	}
	if got := KnownModelIDsForTool("claude"); got[0] != "orbit-custom" || len(got) != len(staticModelIDsForTool("claude"))+1 {
		t.Fatalf("claude = %v, want extra then built-ins", got)
	}
}

// probe = false turns off the CLI but keeps the configured overrides.
func TestIssue2664_ConfigExtraWithProbeOff(t *testing.T) {
	f := installFakeOpenCode(t, fakeOpenCodeOK)
	off := false
	setModelsConfig(t, ModelsSettings{Probe: &off, OpenCode: ModelListSettings{Extra: []string{"local/qwen"}}})
	got := KnownModelIDsForTool("opencode")
	if want := append([]string{"local/qwen"}, staticModelIDsForTool("opencode")...); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if n := f.probeCalls(t); n != 0 {
		t.Fatalf("probe = false still ran the CLI %d times", n)
	}
}

func TestIssue2664_ConfigTOML(t *testing.T) {
	var cfg UserConfig
	data := `
[models]
probe = true

[models.codex]
extra = ["nova-2"]

[models.opencode]
list = ["acme/nova-2", "local/qwen"]
`
	if _, err := toml.Decode(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Models.ProbeEnabled() {
		t.Fatal("probe should stay on")
	}
	if got := cfg.Models.ForTool("codex").Extra; !slices.Equal(got, []string{"nova-2"}) {
		t.Fatalf("codex extra = %v", got)
	}
	if got := cfg.Models.ForTool("opencode").List; !slices.Equal(got, []string{"acme/nova-2", "local/qwen"}) {
		t.Fatalf("opencode list = %v", got)
	}
	// pi sessions take no model, so pi has no table.
	if got := cfg.Models.ForTool("pi"); got.List != nil || got.Extra != nil {
		t.Fatalf("pi has an override: %+v", got)
	}
	if got := cfg.Models.ForTool("shell"); got.List != nil || got.Extra != nil {
		t.Fatalf("unknown tool has an override: %+v", got)
	}
}

// In a long-lived process (after WarmModelCatalog) the first lookup waits,
// so early validation sees the probed list; a later re-probe runs in the
// background while lookups keep the previous answer.
func TestIssue2664_BackgroundReprobeNeverWaits(t *testing.T) {
	f := installFakeOpenCode(t, fakeOpenCodeOK)
	modelProbeBackground.Store(true)
	t.Cleanup(func() { modelProbeBackground.Store(false) })

	if got := KnownModelIDsForTool("opencode"); got[0] != "opencode/big-pickle" {
		t.Fatalf("first lookup should wait for the probe, got %v", got)
	}
	f.writeStub(t, "opencode", `sleep 1; echo acme/nova-3`)
	ageModelProbe(t, "opencode", modelProbeTTL+time.Minute)

	start := time.Now()
	got := KnownModelIDsForTool("opencode")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("re-probe lookup waited %s", elapsed)
	}
	if got[0] != "opencode/big-pickle" {
		t.Fatalf("while re-probing, got %v, want the previous answer", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for KnownModelIDsForTool("opencode")[0] != "acme/nova-3" {
		if time.Now().After(deadline) {
			t.Fatal("background re-probe never landed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Test binaries share one process, so WarmModelCatalog must not switch them
// to background mode, and ResetModelProbeMemo clears it.
func TestIssue2664_BackgroundModeScopedToRealRuns(t *testing.T) {
	installFakeOpenCode(t, fakeOpenCodeOK)
	WarmModelCatalog()
	if modelProbeBackground.Load() {
		t.Fatal("WarmModelCatalog switched a test binary to background mode")
	}
	modelProbeBackground.Store(true)
	ResetModelProbeMemo()
	if modelProbeBackground.Load() {
		t.Fatal("ResetModelProbeMemo must clear background mode")
	}
}

// A failure is remembered on disk for the backoff, so the next short-lived
// process does not wait on a broken CLI again; after it, the probe retries.
func TestIssue2664_FailureSkipsNextProcessWithinBackoff(t *testing.T) {
	f := installFakeOpenCode(t, `sleep 0.2; exit 1`)
	KnownModelIDsForTool("opencode")
	ResetModelProbeMemo() // a new process
	if got := KnownModelIDsForTool("opencode"); !slices.Equal(got, staticModelIDsForTool("opencode")) {
		t.Fatalf("got %v, want the static catalog", got)
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("%d probe runs, want 1 (the failure is cached for the backoff)", n)
	}
	ageModelProbe(t, "opencode", modelProbeFailureBackoff)
	ResetModelProbeMemo()
	KnownModelIDsForTool("opencode")
	if n := f.probeCalls(t); n != 2 {
		t.Fatalf("%d probe runs, want 2 (past the backoff it retries)", n)
	}
}

func TestIssue2664_CommandParsing(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	if got := probeBinaryFromCommand(`OPENCODE_CONFIG="a b.json" opencode --x`, "opencode"); got != "opencode" {
		t.Fatalf("binary = %q", got)
	}
	got := probeEnvFromCommand(`A="a b" B='$HOME/x' C=$HOME/y D=~/z opencode`)
	if want := []string{"A=a b", "B=$HOME/x", "C=/home/u/y", "D=/home/u/z"}; !slices.Equal(got, want) {
		t.Fatalf("env = %q, want %q", got, want)
	}
}

// The probe sees the command's VAR=value prefix (expanded like the shell),
// and a different prefix is a different cache entry.
func TestIssue2664_ProbeUsesCommandEnv(t *testing.T) {
	f := installFakeOpenCode(t, `echo "cfg/$(basename "$OPENCODE_CONFIG")"; echo "home/$(dirname "$OPENCODE_CONFIG" | sed "s#^$HOME#ok#")"`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &UserConfig{}
	cfg.OpenCode.Command = "OPENCODE_CONFIG=~/work.json opencode"
	t.Cleanup(resetUserConfigCache(t, cfg))

	if got := KnownModelIDsForTool("opencode"); !slices.Equal(got[:2], []string{"cfg/work.json", "home/ok"}) {
		t.Fatalf("probe did not get the expanded env: %v", got)
	}

	cfg2 := &UserConfig{}
	cfg2.OpenCode.Command = `OPENCODE_CONFIG="$HOME/other.json" opencode`
	t.Cleanup(resetUserConfigCache(t, cfg2))
	ResetModelProbeMemo()
	if got := KnownModelIDsForTool("opencode"); got[0] != "cfg/other.json" || got[1] != "home/ok" {
		t.Fatalf("got %v, want the other config's answer", got)
	}
	if n := f.probeCalls(t); n != 2 {
		t.Fatalf("%d probe runs, want 2 (a new prefix must not hit the old cache)", n)
	}
	data, err := os.ReadFile(mustOpenCodeCachePath(t))
	if err != nil || strings.Contains(string(data), "OPENCODE_CONFIG") || strings.Contains(string(data), home) {
		t.Fatalf("cache must fingerprint the env, not store it: %s (err %v)", data, err)
	}
}

func mustOpenCodeCachePath(t *testing.T) string {
	t.Helper()
	path, err := modelProbeCachePath("opencode")
	if err != nil {
		t.Fatal(err)
	}
	return path
}
