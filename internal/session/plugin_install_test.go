// Phase 4 tests for plugin auto-install. RFC: docs/rfc/PLUGIN_ATTACH.md §4.6.
//
// All real exec is stubbed via pluginInstallExec to avoid spawning
// claude binaries during unit tests.

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type stubExecCall struct {
	name string
	args []string
}

// withStubPluginExec records every pluginInstallExec invocation and
// returns a controllable stub. The stub returns ("", nil) by default —
// individual tests override responseFn to simulate failure.
func withStubPluginExec(t *testing.T) (*[]stubExecCall, func(func(name string, args []string) ([]byte, error))) {
	t.Helper()
	var (
		mu       sync.Mutex
		recorded []stubExecCall
		respFn   = func(name string, args []string) ([]byte, error) { return nil, nil }
	)
	orig := pluginInstallExec
	pluginInstallExec = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		mu.Lock()
		recorded = append(recorded, stubExecCall{name: name, args: args})
		mu.Unlock()
		return respFn(name, args)
	}
	t.Cleanup(func() { pluginInstallExec = orig })
	setRespFn := func(fn func(name string, args []string) ([]byte, error)) { respFn = fn }
	return &recorded, setRespFn
}

// TestEnsurePluginsInstalled_NoOp_WhenNoPlugins asserts the function
// returns immediately without exec when Instance.Plugins is empty.
func TestEnsurePluginsInstalled_NoOp_WhenNoPlugins(t *testing.T) {
	calls, _ := withStubPluginExec(t)
	inst := &Instance{ID: "x", Tool: "claude"}
	if err := inst.EnsurePluginsInstalled("/fake/profile"); err != nil {
		t.Fatalf("EnsurePluginsInstalled: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("expected zero exec calls; got %d (%v)", len(*calls), *calls)
	}
}

// TestEnsurePluginsInstalled_AttachIsConsent asserts that an attached
// catalog plugin gets installed even when its catalog `auto_install`
// flag is false. Rationale: the user explicitly attached it (via CLI
// or TUI), which is itself the consent signal — we should not silently
// skip the install and leave the worker with `enabledPlugins[X]=true`
// pointing at code that isn't on disk.
func TestEnsurePluginsInstalled_AttachIsConsent(t *testing.T) {
	withTempHome(t)
	writeConfig(t, os.Getenv("HOME"), `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = false
`)
	calls, _ := withStubPluginExec(t)
	inst := &Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus"}}
	_ = inst.EnsurePluginsInstalled(t.TempDir())
	// Expect at least one `plugin install ...` call. There may be a
	// preceding `plugin marketplace add ...` for unknown marketplaces.
	sawInstall := false
	for _, c := range *calls {
		for _, a := range c.args {
			if a == "install" {
				sawInstall = true
			}
		}
	}
	if !sawInstall {
		t.Errorf("attach must trigger plugin install regardless of auto_install flag; got calls=%v", *calls)
	}
}

// TestEnsurePluginsInstalled_SkipsAlreadyInstalled asserts the
// idempotency check — when `<source>/plugins/<source>/<name>/` already
// exists, no exec runs.
func TestEnsurePluginsInstalled_SkipsAlreadyInstalled(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = true
`)
	source := t.TempDir()
	// Pre-create the plugin dir matching claude's real layout:
	// <profile>/plugins/cache/<source>/<name>/<version>/
	pluginDir := filepath.Join(source, "plugins", "cache", "nyldn/claude-octopus", "octopus", "1.0.0")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	calls, _ := withStubPluginExec(t)
	inst := &Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus"}}
	_ = inst.EnsurePluginsInstalled(source)
	if len(*calls) != 0 {
		t.Errorf("already-installed plugin must skip exec; got %d calls (%v)", len(*calls), *calls)
	}
}

// TestEnsurePluginsInstalled_RunsMarketplaceAddThenInstall asserts the
// happy-path order: `claude plugin marketplace add` then
// `claude plugin install <id>`.
func TestEnsurePluginsInstalled_RunsMarketplaceAddThenInstall(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = true
`)
	source := t.TempDir() // empty source — plugin not installed
	calls, _ := withStubPluginExec(t)
	inst := &Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus"}}
	_ = inst.EnsurePluginsInstalled(source)

	if len(*calls) != 2 {
		t.Fatalf("expected 2 exec calls (marketplace add, plugin install); got %d (%v)", len(*calls), *calls)
	}
	if (*calls)[0].name != "claude" || len((*calls)[0].args) < 4 || (*calls)[0].args[1] != "marketplace" {
		t.Errorf("call[0] should be `claude plugin marketplace add ...`; got %v", (*calls)[0])
	}
	if (*calls)[1].name != "claude" || (*calls)[1].args[1] != "install" {
		t.Errorf("call[1] should be `claude plugin install <id>`; got %v", (*calls)[1])
	}
	// Last arg of install MUST be the FQ id "<name>@<source>".
	last := (*calls)[1].args[len((*calls)[1].args)-1]
	if last != "octopus@nyldn/claude-octopus" {
		t.Errorf("install arg: got %q, want %q", last, "octopus@nyldn/claude-octopus")
	}
}

// TestEnsurePluginsInstalled_ContinuesOnInstallError asserts a single
// failed install does not block subsequent plugins (best-effort).
func TestEnsurePluginsInstalled_ContinuesOnInstallError(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = true

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
auto_install = true
`)
	source := t.TempDir()
	calls, setResp := withStubPluginExec(t)
	// Fail every "install" call, succeed marketplace adds.
	setResp(func(name string, args []string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "install" {
			return []byte("simulated install failure"), &simulatedExecErr{}
		}
		return nil, nil
	})

	inst := &Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus", "discord"}}
	_ = inst.EnsurePluginsInstalled(source) // never errors by contract

	// Both plugins must have attempted both marketplace add + install
	// (4 calls total) despite the first install failing.
	if len(*calls) != 4 {
		t.Errorf("expected 4 exec calls (2 plugins × 2 commands); got %d (%v)", len(*calls), *calls)
	}
}

type simulatedExecErr struct{}

func (e *simulatedExecErr) Error() string { return "simulated install failure" }

// TestPluginLockPath_EquivalentSpellingsCollide asserts P4-2: lock key
// must hash on a canonicalized profile path so the SAME profile via two
// equivalent spellings (trailing slash, ".", "..") shares a lock.
// Otherwise same-profile concurrent installs race.
func TestPluginLockPath_EquivalentSpellingsCollide(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	def := &PluginDef{Name: "octopus", Source: "nyldn/claude-octopus"}

	cases := []struct {
		name string
		path string
	}{
		{"plain", "/Users/alice/.claude"},
		{"trailing-slash", "/Users/alice/.claude/"},
		{"redundant-segments", "/Users/alice/./.claude"},
		{"parent-then-back", "/Users/alice/foo/../.claude"},
	}

	var first string
	for i, tc := range cases {
		got, err := pluginLockPath(tc.path, def)
		if err != nil {
			t.Fatalf("[%s] pluginLockPath: %v", tc.name, err)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("[%s] equivalent path %q produced different lock %q (first was %q) — same-profile race regression", tc.name, tc.path, got, first)
		}
	}
}

// TestIsSecretPluginEnv_BlocksKnownTokenPatterns asserts P4-1: any env
// key matching a credential suffix pattern is filtered even when its
// prefix is allow-listed (`npm_config_`, `BUN_`, `NODE_`, `NPM_`).
func TestIsSecretPluginEnv_BlocksKnownTokenPatterns(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// Tokens — must block.
		{"NPM_TOKEN", true},
		{"NPM_AUTH_TOKEN", true},
		{"NODE_AUTH_TOKEN", true},
		{"BUN_AUTH_TOKEN", true},
		{"npm_config_authToken", true},
		{"npm_config__authToken", true},
		{"npm_config__auth", true},
		{"npm_config_password", true},
		{"npm_config_passwd", true},
		{"npm_config_username", true},
		{"npm_config_email", true},
		{"BUN_AUTH_USERNAME", true},
		{"NODE_API_KEY", true},
		{"GITHUB_TOKEN", true}, // not in allow-list anyway, but suffix would catch it
		// Non-secrets in allowed namespace — must NOT block.
		{"npm_config_registry", false},
		{"npm_config_proxy", false},
		{"npm_config_cache", false},
		{"NODE_OPTIONS", false},
		{"NODE_PATH", false},
		{"BUN_INSTALL", false},
		{"NPM_LOGLEVEL", false},
		{"NPM_PREFIX", false},
		{"PATH", false},
	}
	for _, tc := range cases {
		got := isSecretPluginEnv(tc.key)
		if got != tc.want {
			t.Errorf("isSecretPluginEnv(%q): got %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestScrubbedEnvForPluginInstall_DropsCredentialsInAllowedNamespace
// asserts the integration: even though `npm_config_*` is allow-listed
// by prefix, credentials matching the suffix blocklist are still dropped.
func TestScrubbedEnvForPluginInstall_DropsCredentialsInAllowedNamespace(t *testing.T) {
	t.Setenv("npm_config_registry", "https://example.com")
	t.Setenv("npm_config_authToken", "leak-me")
	t.Setenv("NODE_AUTH_TOKEN", "secret")
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=4096")
	t.Setenv("PATH", "/usr/bin")

	scrubbed := scrubbedEnvForPluginInstall()
	got := map[string]string{}
	for _, kv := range scrubbed {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 {
			got[kv[:eq]] = kv[eq+1:]
		}
	}
	// Allowed.
	for _, k := range []string{"npm_config_registry", "NODE_OPTIONS", "PATH"} {
		if _, ok := got[k]; !ok {
			t.Errorf("expected %q to pass scrub; got keys: %v", k, mapKeysSorted(got))
		}
	}
	// Blocked.
	for _, k := range []string{"npm_config_authToken", "NODE_AUTH_TOKEN"} {
		if _, leaked := got[k]; leaked {
			t.Errorf("credential %q leaked through scrub; got keys: %v", k, mapKeysSorted(got))
		}
	}
}

func mapKeysSorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPluginLockPath_IncludesProfileHashAndFlattensSourceSlash asserts
// the lock filename embeds an 8-char profile hash (P3-1 fix: per-profile
// lock scoping) AND flattens owner/repo slashes in source.
func TestPluginLockPath_IncludesProfileHashAndFlattensSourceSlash(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	def := &PluginDef{Name: "octopus", Source: "nyldn/claude-octopus"}

	pathA, err := pluginLockPath("/Users/alice/.claude", def)
	if err != nil {
		t.Fatalf("pluginLockPath A: %v", err)
	}
	pathB, err := pluginLockPath("/Users/alice/.claude-work", def)
	if err != nil {
		t.Fatalf("pluginLockPath B: %v", err)
	}

	// Different profiles → different lock paths (the whole point).
	if pathA == pathB {
		t.Errorf("lock paths for different profiles must differ; got %q == %q", pathA, pathB)
	}
	// Filename shape: plugin-<8hex>-<safeSource>-<name>.lock
	baseA := filepath.Base(pathA)
	if !strings.HasPrefix(baseA, "plugin-") || !strings.HasSuffix(baseA, "-nyldn--claude-octopus-octopus.lock") {
		t.Errorf("lock filename shape unexpected: %q", baseA)
	}
	// Same profile + same plugin → deterministic.
	pathA2, _ := pluginLockPath("/Users/alice/.claude", def)
	if pathA != pathA2 {
		t.Errorf("lock path must be deterministic for same (profile, def); got %q != %q", pathA, pathA2)
	}
}

// TestEnsurePluginsInstalled_LockHeldRetries asserts a held lock does
// NOT cause a panic — it's a best-effort path that logs and skips.
func TestEnsurePluginsInstalled_LockHeldRetries(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = true
`)
	calls, _ := withStubPluginExec(t)

	// Stub the lock acquirer to always fail.
	orig := pluginLockAcquireFn
	pluginLockAcquireFn = func(path string) (func(), error) {
		return nil, &simulatedExecErr{}
	}
	t.Cleanup(func() { pluginLockAcquireFn = orig })

	inst := &Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus"}}
	if err := inst.EnsurePluginsInstalled(t.TempDir()); err != nil {
		t.Fatalf("EnsurePluginsInstalled must never error; got %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("lock acquisition failure must short-circuit before exec; got %d calls", len(*calls))
	}
}
