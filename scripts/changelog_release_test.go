package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scripts/changelog-release.sh moves CHANGELOG.md's Unreleased lines under
// a release heading, and its --check mode is what release-readiness and
// this package's repo check use.

const changelogPreamble = "# Changelog\n\nAll notable changes.\n\n"

// runChangelogRelease writes content to a temp CHANGELOG.md, runs the
// script on it with args, and returns the exit code, output and the file
// afterwards.
func runChangelogRelease(t *testing.T, content string, args ...string) (int, string, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runChangelogScript(t, append([]string{"--file", file}, args...)...)
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return code, out, string(after)
}

func runChangelogScript(t *testing.T, args ...string) (string, int) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", append([]string{filepath.Join(wd, "changelog-release.sh")}, args...)...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		t.Fatalf("run script: %v\n%s", err, out)
	}
	return string(out), 0
}

func TestChangelogRelease_MovesUnreleasedUnderTheVersion(t *testing.T) {
	in := changelogPreamble + "## [Unreleased]\n\n### Fixes\n\n- Switching sessions no longer stutters (#1).\n\n\n## [1.0.0] - 2027-01-10\n\n- Old.\n"
	want := changelogPreamble + "## [Unreleased]\n\n## [1.0.1] - 2027-01-15\n\n### Fixes\n\n- Switching sessions no longer stutters (#1).\n\n## [1.0.0] - 2027-01-10\n\n- Old.\n"
	code, out, got := runChangelogRelease(t, in, "v1.0.1", "2027-01-15")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got != want {
		t.Fatalf("CHANGELOG.md after release:\n%s\nwant:\n%s", got, want)
	}

	// Unreleased last in the file (a first release) works the same way.
	code, out, got = runChangelogRelease(t, changelogPreamble+"## [Unreleased]\n- First.\n", "0.1.0", "2027-01-15")
	if want := changelogPreamble + "## [Unreleased]\n\n## [0.1.0] - 2027-01-15\n\n- First.\n\n"; code != 0 || got != want {
		t.Fatalf("exit %d, file:\n%q\nwant:\n%q\n%s", code, got, want, out)
	}
}

func TestChangelogRelease_DefaultDateIsTodayInBerlin(t *testing.T) {
	code, out, got := runChangelogRelease(t, changelogPreamble+"## [Unreleased]\n\n- A change.\n", "1.0.1")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !regexp.MustCompile(`(?m)^## \[1\.0\.1\] - \d{4}-\d{2}-\d{2}$`).MatchString(got) {
		t.Fatalf("no dated 1.0.1 heading:\n%s", got)
	}
}

func TestChangelogRelease_RefusesAndLeavesTheFileAlone(t *testing.T) {
	for _, tc := range []struct {
		name, content, version, want string
		code                         int
	}{
		{"empty Unreleased", changelogPreamble + "## [Unreleased]\n\n\n## [1.0.0] - 2027-01-10\n", "1.0.1", "nothing under '## [Unreleased]'", 1},
		{"version already released", changelogPreamble + "## [Unreleased]\n\n- New.\n\n## [1.0.1] - 2027-01-10\n", "1.0.1", "already has a '## [1.0.1]' heading", 1},
		{"no Unreleased", changelogPreamble + "## [1.0.0] - 2027-01-10\n", "1.0.1", "found 0", 1},
		{"two Unreleased", changelogPreamble + "## [Unreleased]\n- a\n## [Unreleased]\n- b\n", "1.0.1", "found 2", 1},
		{"not a version", changelogPreamble + "## [Unreleased]\n- a\n", "1.0", "is not X.Y.Z", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, got := runChangelogRelease(t, tc.content, tc.version, "2027-01-15")
			if code != tc.code || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, tc.code, tc.want, out)
			}
			if got != tc.content {
				t.Fatalf("file changed on refusal:\n%s", got)
			}
		})
	}
}

func TestChangelogRelease_Check(t *testing.T) {
	ok := changelogPreamble + "## [Unreleased]\n\n## [1.0.10] - 2027-01-15\n\n- A change.\n"
	for _, tc := range []struct {
		name, content string
		args          []string
		code          int
	}{
		{"Unreleased only", ok, nil, 0},
		{"version present", ok, []string{"1.0.10"}, 0},
		{"v prefix", ok, []string{"v1.0.10"}, 0},
		{"version missing", ok, []string{"1.0.11"}, 1},
		{"prefix of another version", ok, []string{"1.0.1"}, 1},
		{"no Unreleased", changelogPreamble + "## [1.0.10] - 2027-01-15\n", []string{"1.0.10"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := runChangelogRelease(t, tc.content, append([]string{"--check"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d\n%s", code, tc.code, out)
			}
		})
	}
}

// The repo's own CHANGELOG.md keeps an Unreleased section, and the Version
// in cmd/agent-deck/main.go has a heading. On a release tag the release
// workflow requires the tag to equal that Version, so this is the check that
// every tagged version has a CHANGELOG entry.
func TestChangelog_RepoHasUnreleasedAndAnEntryForTheVersion(t *testing.T) {
	main, err := os.ReadFile(filepath.Join("..", "cmd", "agent-deck", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^var Version = "([0-9][0-9.]*)"`).FindSubmatch(main)
	if m == nil {
		t.Fatal(`no var Version = "X.Y.Z" in cmd/agent-deck/main.go`)
	}
	out, code := runChangelogScript(t, "--file", filepath.Join("..", "CHANGELOG.md"), "--check", string(m[1]))
	if code != 0 {
		t.Fatalf("CHANGELOG.md check for v%s failed (exit %d):\n%s", m[1], code, out)
	}
}
