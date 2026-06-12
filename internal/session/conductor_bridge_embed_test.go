package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// canonicalBridgePath returns the path to the one canonical bridge source,
// internal/session/conductor_bridge.py, relative to this test file.
func canonicalBridgePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "conductor_bridge.py")
}

// TestEmbeddedBridgeMatchesCanonical asserts the embedded bytes equal the
// canonical on-disk file. There is exactly one canonical bridge.py in the repo
// (internal/session/conductor_bridge.py); go:embed pulls it in directly, so
// this is mostly insurance against build-cache weirdness. The maintainer
// explicitly asked for it. If it ever fails, the embedded var and the file have
// somehow diverged within a build.
func TestEmbeddedBridgeMatchesCanonical(t *testing.T) {
	canonical, err := os.ReadFile(canonicalBridgePath(t))
	if err != nil {
		t.Fatalf("read canonical conductor_bridge.py: %v", err)
	}
	if conductorBridgePy != string(canonical) {
		t.Errorf("embedded conductorBridgePy differs from internal/session/conductor_bridge.py " +
			"(the single canonical source); the go:embed bytes and the on-disk file disagree")
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
// — the behavior that lets config.toml reference $TELEGRAM_BOT_TOKEN etc. — and
// that the #452 _drain_queue ships in the SAME file (both halves of the union).
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
