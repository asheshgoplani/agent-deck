// Issue #1163 Change 2 — the forbidigo lint rule that bans raw os.Environ()
// in spawn-path packages must actually fire, so a future caller cannot
// reintroduce the CLAUDE_CONFIG_DIR leak by bypassing internal/childenv.
package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForbidigo_BansRawOSEnvironInSpawnPath(t *testing.T) {
	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Skip("golangci-lint not on PATH; the rule is enforced in CI")
	}

	// Repo root is two levels up from internal/session.
	wd, err := os.Getwd()
	require.NoError(t, err)
	repoRoot := filepath.Dir(filepath.Dir(wd))

	// Drop a fixture that uses os.Environ() directly in internal/session,
	// without the //nolint:forbidigo escape hatch the legitimate uses carry.
	fixture := filepath.Join(wd, "zz_issue1163_forbidigo_fixture.go")
	const src = `package session

import "os"

// fixtureUsesRawEnviron exists only for TestForbidigo; it must trip the lint.
func fixtureUsesRawEnviron() []string { return os.Environ() }
`
	require.NoError(t, os.WriteFile(fixture, []byte(src), 0o644))
	t.Cleanup(func() { _ = os.Remove(fixture) })

	// Run only forbidigo for speed; settings (forbid patterns) still load
	// from .golangci.yml.
	cmd := exec.Command(bin, "run", "--default=none", "--enable=forbidigo", "./internal/session/")
	cmd.Dir = repoRoot
	out, _ := cmd.CombinedOutput()

	got := string(out)
	require.Contains(t, got, "zz_issue1163_forbidigo_fixture.go",
		"forbidigo must flag the fixture file; output:\n%s", got)
	require.Contains(t, got, "os.Environ",
		"forbidigo message must name os.Environ; output:\n%s", got)
	require.Contains(t, strings.ToLower(got), "forbidden",
		"forbidigo must report the use as forbidden; output:\n%s", got)
}
