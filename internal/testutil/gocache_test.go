package testutil

import (
	"os/exec"
	"strings"
	"testing"
)

func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// The caches the go command resolves before HOME moves are the ones a
// child `go build` still uses after it moves.
func TestPinGoCachesSurvivesHomeMove(t *testing.T) {
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "XDG_CACHE_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
	wantCache, wantMod := goEnv(t, "GOCACHE"), goEnv(t, "GOMODCACHE")
	PinGoCaches()
	t.Setenv("HOME", t.TempDir()) // what IsolateHome does next
	if got := goEnv(t, "GOCACHE"); got != wantCache {
		t.Errorf("GOCACHE after HOME moved = %q, want %q", got, wantCache)
	}
	if got := goEnv(t, "GOMODCACHE"); got != wantMod {
		t.Errorf("GOMODCACHE after HOME moved = %q, want %q", got, wantMod)
	}
}

// A cache the caller already chose is kept.
func TestPinGoCachesKeepsCallerValues(t *testing.T) {
	t.Setenv("GOCACHE", "/opt/ci/gocache")
	t.Setenv("GOMODCACHE", "/opt/ci/gomod")
	PinGoCaches()
	if got := goEnv(t, "GOCACHE"); got != "/opt/ci/gocache" {
		t.Errorf("GOCACHE = %q, want the caller's", got)
	}
	if got := goEnv(t, "GOMODCACHE"); got != "/opt/ci/gomod" {
		t.Errorf("GOMODCACHE = %q, want the caller's", got)
	}
}
