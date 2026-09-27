package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests for the installed-CLI model probe (#2388). Each case installs a fake
// `codex` shell stub on PATH; nothing here runs a real CLI or a billed prompt.

const fakeCodexModelsJSON = `{"models":[
 {"slug":"gpt-7-nova","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"hyper"}]},
 {"slug":"gpt-6-astra","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}]},
 {"slug":"gpt-internal","visibility":"hide","priority":0,"supported_reasoning_levels":[{"effort":"low"}]},
 {"slug":"gpt-5.5","visibility":"list","priority":12,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}]}
]}`

type fakeCodex struct {
	dir     string
	fixture string
	calls   string
}

// installFakeCodex puts a `codex` stub first on PATH, points the probe cache
// at a temp dir and opts this test binary into probing.
func installFakeCodex(t *testing.T, body string) *fakeCodex {
	t.Helper()
	dir := t.TempDir()
	f := &fakeCodex{dir: dir, fixture: filepath.Join(dir, "models.json"), calls: filepath.Join(dir, "calls")}
	if err := os.WriteFile(f.fixture, []byte(fakeCodexModelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writeStub(t, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("FAKE_CODEX_FIXTURE", f.fixture)
	t.Setenv("FAKE_CODEX_CALLS", f.calls)
	t.Setenv(modelProbeTestEnv, "1")
	restoreCfg := resetUserConfigCache(t, &UserConfig{})
	ResetModelProbeMemo()
	t.Cleanup(func() {
		restoreCfg()
		ResetModelProbeMemo()
	})
	return f
}

func (f *fakeCodex) writeStub(t *testing.T, body string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\n" +
		"echo \"$*\" >> \"$FAKE_CODEX_CALLS\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(f.dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeCodex) probeCalls(t *testing.T) int {
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

const fakeCodexOK = `cat "$FAKE_CODEX_FIXTURE"`

func TestModelProbe_CodexSuccessMergesProbedFirst(t *testing.T) {
	installFakeCodex(t, fakeCodexOK)

	models := KnownModelIDsForTool("codex")
	if want := []string{"gpt-7-nova", "gpt-6-astra", "gpt-5.5"}; !slices.Equal(models[:3], want) {
		t.Fatalf("probed models first: got %v, want prefix %v", models[:3], want)
	}
	if slices.Contains(models, "gpt-internal") {
		t.Fatal("hidden models must not be suggested")
	}
	for _, id := range staticModelIDsForTool("codex") {
		if !slices.Contains(models, id) {
			t.Fatalf("static id %q dropped by the merge; the static catalog is the floor", id)
		}
	}
	seen := map[string]bool{}
	for _, id := range models {
		if seen[id] {
			t.Fatalf("merged list has duplicate %q: %v", id, models)
		}
		seen[id] = true
	}

	efforts := LaunchReasoningEffortsForTool("codex")
	if !slices.Equal(efforts[:len(codexReasoningEfforts)], codexReasoningEfforts) || !slices.Contains(efforts, "hyper") {
		t.Fatalf("efforts = %v, want static list then probed extras", efforts)
	}
	if err := ValidateLaunchReasoningEffort("codex", "hyper"); err != nil {
		t.Fatalf("probed effort rejected: %v", err)
	}
}

func TestModelProbe_PerModelEffortValidation(t *testing.T) {
	installFakeCodex(t, fakeCodexOK)

	if err := ValidateLaunchReasoningEffortForModel("codex", "gpt-5.5", "xhigh"); err != nil {
		t.Fatalf("gpt-5.5 xhigh: %v", err)
	}
	err := ValidateLaunchReasoningEffortForModel("codex", "gpt-5.5", "ultra")
	if err == nil || !strings.Contains(err.Error(), "gpt-5.5") {
		t.Fatalf("gpt-5.5 ultra: err = %v, want a per-model rejection", err)
	}
	// A model the probe does not describe falls back to the tool-wide list.
	if err := ValidateLaunchReasoningEffortForModel("codex", "my-proxy-model", "ultra"); err != nil {
		t.Fatalf("unknown model uses tool-wide list: %v", err)
	}

	inst := NewInstanceWithTool("per-model", t.TempDir(), "codex")
	if err := inst.ApplyLaunchModel("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if err := inst.ApplyLaunchReasoningEffort("ultra"); err == nil {
		t.Fatal("ApplyLaunchReasoningEffort accepted an effort the selected model lacks")
	}
	if err := inst.ApplyLaunchReasoningEffort("high"); err != nil {
		t.Fatalf("ApplyLaunchReasoningEffort(high): %v", err)
	}

	perModel := LaunchModelEffortsForTool("codex")
	if !slices.Equal(perModel["gpt-7-nova"], []string{"low", "medium", "high", "hyper"}) {
		t.Fatalf("LaunchModelEffortsForTool = %v", perModel)
	}
	if _, ok := perModel["gpt-internal"]; ok {
		t.Fatal("hidden models must not be advertised in the per-model lists")
	}
	// A hidden model chosen explicitly still validates against its own levels.
	if err := ValidateLaunchReasoningEffortForModel("codex", "gpt-internal", "high"); err == nil {
		t.Fatal("hidden model accepted an effort it does not list")
	}
}

func TestModelProbe_FailureFallsBackToStatic(t *testing.T) {
	cases := map[string]string{
		"non-zero exit":  `echo boom >&2; exit 3`,
		"garbage output": `echo 'not json'`,
		"wrong shape":    `echo '{"data":[]}'`,
		"bad slug":       `echo '{"models":[{"slug":"rm -rf /","visibility":"list","priority":1}]}'`,
		"no listed":      `echo '{"models":[{"slug":"a","visibility":"hide","priority":1}]}'`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			installFakeCodex(t, body)
			if got := KnownModelIDsForTool("codex"); !slices.Equal(got, staticModelIDsForTool("codex")) {
				t.Fatalf("got %v, want the static catalog", got)
			}
			if got := LaunchReasoningEffortsForTool("codex"); !slices.Equal(got, codexReasoningEfforts) {
				t.Fatalf("efforts %v, want static", got)
			}
			if _, err := os.Stat(mustModelProbeCachePath(t)); !os.IsNotExist(err) {
				t.Fatalf("a failed probe must not be cached on disk (stat err %v)", err)
			}
		})
	}
}

func TestModelProbe_TimeoutFallsBackToStatic(t *testing.T) {
	installFakeCodex(t, `sleep 5; cat "$FAKE_CODEX_FIXTURE"`)
	prev := modelProbeTimeout
	modelProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { modelProbeTimeout = prev })

	start := time.Now()
	got := KnownModelIDsForTool("codex")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("probe blocked %s despite the timeout", elapsed)
	}
	if !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("got %v, want the static catalog", got)
	}
}

func TestModelProbe_MissingCLIFallsBackToStatic(t *testing.T) {
	installFakeCodex(t, fakeCodexOK)
	t.Setenv("PATH", t.TempDir())
	if got := KnownModelIDsForTool("codex"); !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("got %v, want the static catalog", got)
	}
}

func TestModelProbe_CacheHitAndVersionChange(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)

	KnownModelIDsForTool("codex")
	KnownModelIDsForTool("codex")
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("in-memory memo: %d probe runs, want 1", n)
	}

	// A new process (empty memo) reads the disk cache instead of probing.
	ResetModelProbeMemo()
	if got := KnownModelIDsForTool("codex"); got[0] != "gpt-7-nova" {
		t.Fatalf("disk cache hit returned %v", got)
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("disk cache: %d probe runs, want 1", n)
	}
	data, err := os.ReadFile(mustModelProbeCachePath(t))
	if err != nil || !strings.Contains(string(data), "codex-cli 0.0.1") {
		t.Fatalf("cache file should record the CLI version: %s (err %v)", data, err)
	}

	// Upgrading the CLI changes the binary (mtime/size): the cache misses.
	f.writeStub(t, "# upgraded\n"+fakeCodexOK)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.dir, "codex"), future, future); err != nil {
		t.Fatal(err)
	}
	ResetModelProbeMemo()
	KnownModelIDsForTool("codex")
	if n := f.probeCalls(t); n != 2 {
		t.Fatalf("after upgrade: %d probe runs, want 2", n)
	}
}

func TestModelProbe_CacheTTL(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)
	KnownModelIDsForTool("codex")

	key, err := modelProbeBinaryKey("codex")
	if err != nil {
		t.Fatal(err)
	}
	if readModelProbeCache("codex", key, time.Now().Add(modelProbeTTL+time.Minute)) != nil {
		t.Fatal("a cache entry older than the TTL must miss")
	}
	if readModelProbeCache("codex", key, time.Now()) == nil {
		t.Fatal("a fresh cache entry must hit")
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("%d probe runs, want 1", n)
	}
}

// ageModelProbeMemo moves the codex memo into the past, as if d had passed.
func ageModelProbeMemo(t *testing.T, d time.Duration) {
	t.Helper()
	modelProbeMu.Lock()
	defer modelProbeMu.Unlock()
	m := modelProbeMemos["codex"]
	if m == nil {
		t.Fatal("no codex memo to age")
	}
	m.checkedAt = m.checkedAt.Add(-d)
	m.probedAt = m.probedAt.Add(-d)
}

// A failed probe (e.g. a cold-start timeout in the TUI's startup warm-up) is
// remembered only briefly: within the backoff lookups stay cheap, after it the
// probe runs again and picks up the recovered CLI.
func TestModelProbe_FailureRetriedAfterBackoff(t *testing.T) {
	f := installFakeCodex(t, `if [ -f "$FAKE_CODEX_OK" ]; then cat "$FAKE_CODEX_FIXTURE"; else exit 3; fi`)
	okFlag := filepath.Join(f.dir, "ok")
	t.Setenv("FAKE_CODEX_OK", okFlag)

	if got := KnownModelIDsForTool("codex"); !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("first probe should fail to the static catalog, got %v", got)
	}
	if err := os.WriteFile(okFlag, nil, 0o644); err != nil { // CLI recovers
		t.Fatal(err)
	}

	// Inside the backoff the failure is still memoized: no re-run per lookup.
	ageModelProbeMemo(t, modelProbeFailureBackoff/2)
	KnownModelIDsForTool("codex")
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("within the backoff: %d probe runs, want 1", n)
	}

	ageModelProbeMemo(t, modelProbeFailureBackoff)
	if got := KnownModelIDsForTool("codex"); got[0] != "gpt-7-nova" {
		t.Fatalf("after the backoff the recovered CLI should be probed, got %v", got)
	}
	if n := f.probeCalls(t); n != 2 {
		t.Fatalf("after the backoff: %d probe runs, want 2", n)
	}
}

// Review repro (#2408): one failed probe must not stick for the whole success
// TTL in a long-lived process once the CLI answers again.
func TestReviewRepro_FailedProbeStickyForTTL(t *testing.T) {
	f := installFakeCodex(t, `if [ -f "$FAKE_CODEX_OK" ]; then cat "$FAKE_CODEX_FIXTURE"; else exit 3; fi`)
	okFlag := filepath.Join(f.dir, "ok")
	t.Setenv("FAKE_CODEX_OK", okFlag)

	if got := KnownModelIDsForTool("codex"); got[0] == "gpt-7-nova" {
		t.Fatal("setup: first probe should fail")
	}
	if err := os.WriteFile(okFlag, nil, 0o644); err != nil { // CLI recovers
		t.Fatal(err)
	}
	ageModelProbeMemo(t, 10*time.Minute)

	got := KnownModelIDsForTool("codex")
	if got[0] != "gpt-7-nova" {
		t.Fatalf("after the CLI recovered and 10 min passed, still static list (probe runs=%d)", f.probeCalls(t))
	}
}

// A successful probe stays cached well past the failure backoff: only the
// success TTL (or a binary change) re-probes.
func TestModelProbe_SuccessKeptPastFailureBackoff(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)
	KnownModelIDsForTool("codex")
	ageModelProbeMemo(t, 10*time.Minute)
	if got := KnownModelIDsForTool("codex"); got[0] != "gpt-7-nova" {
		t.Fatalf("got %v, want the probed list", got)
	}
	if n := f.probeCalls(t); n != 1 {
		t.Fatalf("%d probe runs, want 1 (success must stay cached)", n)
	}
}

func TestModelProbe_DisabledByConfig(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)
	off := false
	cfg := &UserConfig{}
	cfg.Models.Probe = &off
	restore := resetUserConfigCache(t, cfg)
	t.Cleanup(restore)

	if got := KnownModelIDsForTool("codex"); !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("probe = false must keep the static catalog, got %v", got)
	}
	if got := LaunchReasoningEffortsForTool("codex"); !slices.Equal(got, codexReasoningEfforts) {
		t.Fatalf("probe = false must keep static efforts, got %v", got)
	}
	if err := ValidateLaunchReasoningEffortForModel("codex", "gpt-5.5", "ultra"); err != nil {
		t.Fatalf("probe = false must not narrow per model: %v", err)
	}
	if LaunchModelEffortsForTool("codex") != nil {
		t.Fatal("probe = false must report no per-model efforts")
	}
	if n := f.probeCalls(t); n != 0 {
		t.Fatalf("probe = false still ran the CLI %d times", n)
	}
}

func TestModelProbe_OffInTestsByDefault(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)
	t.Setenv(modelProbeTestEnv, "")
	if got := KnownModelIDsForTool("codex"); !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("got %v", got)
	}
	if n := f.probeCalls(t); n != 0 {
		t.Fatalf("test binaries must not probe unless opted in; ran %d times", n)
	}
}

func TestModelProbe_ClaudeAndGeminiStayStatic(t *testing.T) {
	installFakeCodex(t, fakeCodexOK)
	for _, tool := range []string{"claude", "gemini", "opencode"} {
		if got := KnownModelIDsForTool(tool); !slices.Equal(got, staticModelIDsForTool(tool)) {
			t.Fatalf("%s: got %v, want static", tool, got)
		}
	}
	if got := LaunchReasoningEffortsForTool("claude"); !slices.Equal(got, claudeReasoningEfforts) {
		t.Fatalf("claude efforts = %v", got)
	}
}

func TestParseCodexDebugModels_Strict(t *testing.T) {
	got, err := parseCodexDebugModels([]byte(fakeCodexModelsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Models, []string{"gpt-7-nova", "gpt-6-astra", "gpt-5.5"}) {
		t.Fatalf("models = %v (want listed only, by priority)", got.Models)
	}
	if !slices.Equal(got.Efforts["gpt-internal"], []string{"low"}) {
		t.Fatalf("hidden models keep their efforts for validation: %v", got.Efforts)
	}
	for name, input := range map[string]string{
		"missing priority":   `{"models":[{"slug":"a","visibility":"list"}]}`,
		"unknown visibility": `{"models":[{"slug":"a","visibility":"maybe","priority":1}]}`,
		"bad effort":         `{"models":[{"slug":"a","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"a b"}]}]}`,
		"null models":        `{"models":null}`,
	} {
		if _, err := parseCodexDebugModels([]byte(input)); err == nil {
			t.Errorf("%s: parser accepted %s", name, input)
		}
	}
}

func mustModelProbeCachePath(t *testing.T) string {
	t.Helper()
	path, err := modelProbeCachePath("codex")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModelProbe_UsesConfiguredCodexCommand(t *testing.T) {
	f := installFakeCodex(t, fakeCodexOK)
	// Only a `codex-v2` binary answers; the plain `codex` stub is gone.
	if err := os.Rename(filepath.Join(f.dir, "codex"), filepath.Join(f.dir, "codex-v2")); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{}
	cfg.Codex.Command = "CODEX_HOME=/tmp/codex-work codex-v2 --profile work"
	t.Cleanup(resetUserConfigCache(t, cfg))

	if got := codexProbeBinary(); got != "codex-v2" {
		t.Fatalf("codexProbeBinary() = %q, want codex-v2", got)
	}
	if got := KnownModelIDsForTool("codex"); got[0] != "gpt-7-nova" {
		t.Fatalf("probe did not use the configured command: %v", got)
	}
}

func TestModelProbe_ConcurrentCallerDoesNotWait(t *testing.T) {
	installFakeCodex(t, `sleep 1; cat "$FAKE_CODEX_FIXTURE"`)
	prev := modelProbeTimeout
	modelProbeTimeout = 5 * time.Second
	t.Cleanup(func() { modelProbeTimeout = prev })

	done := make(chan []string)
	go func() { done <- KnownModelIDsForTool("codex") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		modelProbeMu.Lock()
		inFlight := modelProbeInFlight["codex"]
		modelProbeMu.Unlock()
		if inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	got := KnownModelIDsForTool("codex")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("second caller waited %s on the running probe", elapsed)
	}
	if !slices.Equal(got, staticModelIDsForTool("codex")) {
		t.Fatalf("while probing, got %v, want the static catalog", got)
	}
	if first := <-done; first[0] != "gpt-7-nova" {
		t.Fatalf("probing caller got %v", first)
	}
}
