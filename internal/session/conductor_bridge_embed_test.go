package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRootFromTest returns the repository root relative to this test file.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/session/<file> -> repo root is three dirs up.
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// TestEmbeddedBridgeMatchesCanonical is the anti-drift guarantee. The bridge
// script has a single source of truth: conductor/bridge.py (imported directly
// by conductor/tests). internal/session/conductor_bridge.py is a byte-identical
// mirror that is //go:embed-ed into conductorBridgePy and shipped by
// InstallBridgeScript / update.UpdateBridgePy.
//
// Historically these two copies were hand-maintained and drifted: the embedded
// copy carried #1386 (env-var secret resolution) but not #452 (queue/async),
// while the standalone carried #452 but not #1386 — so the tested file was
// never the deployed file and fixes silently failed to ship. This test fails
// loudly if the embedded/deployed bytes ever diverge from the tested file
// again. Run `go generate ./internal/session/` to re-sync after editing the
// canonical conductor/bridge.py.
func TestEmbeddedBridgeMatchesCanonical(t *testing.T) {
	repoRoot := repoRootFromTest(t)

	canonical, err := os.ReadFile(filepath.Join(repoRoot, "conductor", "bridge.py"))
	if err != nil {
		t.Fatalf("read canonical conductor/bridge.py: %v", err)
	}
	mirror, err := os.ReadFile(filepath.Join(repoRoot, "internal", "session", "conductor_bridge.py"))
	if err != nil {
		t.Fatalf("read mirror internal/session/conductor_bridge.py: %v", err)
	}

	if string(mirror) != string(canonical) {
		t.Errorf("internal/session/conductor_bridge.py has drifted from conductor/bridge.py.\n" +
			"Run `go generate ./internal/session/` to re-sync the embedded mirror.")
	}
	// The embedded value is what actually deploys; it must equal the canonical
	// tested file byte-for-byte.
	if conductorBridgePy != string(canonical) {
		t.Errorf("embedded conductorBridgePy differs from conductor/bridge.py.\n" +
			"Run `go generate ./internal/session/` to re-sync the embedded mirror.")
	}
}

// TestEmbeddedBridgeParsesAsPython is a smoke test that the bytes we deploy are
// valid Python (mirrors the conductor/tests + python-compat CI gate, but on the
// embedded value rather than the on-disk file).
func TestEmbeddedBridgeParsesAsPython(t *testing.T) {
	py := findPython3()
	if py == "" {
		t.Skip("python3 not found; cannot syntax-check embedded bridge")
	}
	cmd := exec.Command(py, "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(conductorBridgePy)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("embedded bridge.py does not parse as Python: %v\n%s", err, out)
	}
}

// TestEmbeddedBridgeResolvesSecret verifies the deployed bridge carries the
// #1386 env-var secret resolution (one half of the union). It imports the
// embedded bytes as a module and checks _resolve_secret("$VAR") reads os.environ
// — the behavior that lets config.toml reference $TELEGRAM_BOT_TOKEN etc.
func TestEmbeddedBridgeResolvesSecret(t *testing.T) {
	py := findPython3()
	if py == "" {
		t.Skip("python3 not found; cannot exercise embedded load_config")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bridge.py"), []byte(conductorBridgePy), 0o644); err != nil {
		t.Fatalf("write temp bridge.py: %v", err)
	}

	script := `
import sys, types
# Stub optional third-party deps so the smoke test doesn't require them.
sys.modules.setdefault("toml", types.SimpleNamespace(load=lambda *a, **k: {}))
sys.path.insert(0, sys.argv[1])
import bridge
assert hasattr(bridge, "_resolve_secret"), "embedded bridge missing _resolve_secret (#1386)"
assert bridge._resolve_secret("$AD_TEST_SECRET") == "sentinel-value", "env $VAR not resolved"
assert bridge._resolve_secret("${AD_TEST_SECRET}") == "sentinel-value", "env ${VAR} not resolved"
assert bridge._resolve_secret("plain") == "plain", "plain value should pass through"
# The #452 union half must also be present in the SAME deployed file.
assert hasattr(bridge, "_drain_queue"), "embedded bridge missing _drain_queue (#452)"
print("OK")
`
	cmd := exec.Command(py, "-c", script, dir)
	cmd.Env = append(os.Environ(), "AD_TEST_SECRET=sentinel-value")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("embedded bridge _resolve_secret smoke failed: %v\n%s", err, out)
	}
}
