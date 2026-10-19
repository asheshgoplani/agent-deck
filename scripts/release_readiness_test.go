package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release-readiness script refuses a tag the release rules forbid: a
// tag within 24 h of the previous one that does not fix it, a Version that
// is not bumped, an update/auto-install/deploy change merged less than 12 h
// ago, and a candidate whose update path end-to-end test did not pass.
// Each case runs the real script against a throwaway git repo whose commit
// and tag times are set relative to a fixed "now".

const readinessNow = 1_800_000_000 // fixed "now" for the script (unix)

type readinessRepo struct {
	t      *testing.T
	dir    string
	runner string // RELEASE_READINESS_RUNNER; empty leaves it unset
	path   string // PATH for the script; empty keeps the test's PATH
}

func newReadinessRepo(t *testing.T) *readinessRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	r := &readinessRepo{t: t, dir: dir}
	r.git(0, "init", "-q", "-b", "main")
	r.runner = r.stubRunner("--- PASS: TestReleaseReadiness_UpdatePath (1.00s)\nok  \tgithub.com/x/internal/releasetests\t1.0s", 0)
	return r
}

// git runs git in the repo with author, committer and tagger time set to
// hoursAgo before readinessNow.
func (r *readinessRepo) git(hoursAgo int, args ...string) string {
	r.t.Helper()
	at := fmt.Sprintf("@%d +0000", readinessNow-hoursAgo*3600)
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+at, "GIT_COMMITTER_DATE="+at, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commit writes path (relative) and commits it hoursAgo.
func (r *readinessRepo) commit(hoursAgo int, path, content, subject string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git(hoursAgo, "add", path)
	r.git(hoursAgo, "commit", "-q", "-m", subject)
}

func (r *readinessRepo) setVersion(hoursAgo int, v string) {
	r.t.Helper()
	r.commit(hoursAgo, "cmd/agent-deck/main.go", "package main\n\nvar Version = \""+v+"\" // set at build time\n", "chore: release v"+v)
}

func (r *readinessRepo) tag(hoursAgo int, name string) {
	r.t.Helper()
	r.git(hoursAgo, "tag", "-a", "-m", name, name)
}

// stubRunner is a go test runner that prints out and exits with code.
func (r *readinessRepo) stubRunner(out string, code int) string {
	r.t.Helper()
	p := filepath.Join(r.t.TempDir(), "runner.sh")
	body := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit %d\n", out, code)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// run runs the script and returns its exit code and output.
func (r *readinessRepo) run(args ...string) (int, string) {
	r.t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(wd, "release-readiness.sh")}, args...)...)
	cmd.Dir = r.dir
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "RELEASE_READINESS_RUNNER=") && !(r.path != "" && strings.HasPrefix(kv, "PATH=")) {
			env = append(env, kv)
		}
	}
	if r.runner != "" {
		env = append(env, "RELEASE_READINESS_RUNNER="+r.runner)
	}
	if r.path != "" {
		env = append(env, "PATH="+r.path)
	}
	cmd.Env = append(env,
		fmt.Sprintf("RELEASE_READINESS_NOW=%d", readinessNow),
		"TMPDIR="+r.t.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	if err != nil {
		r.t.Fatalf("run script: %v\n%s", err, out)
	}
	return 0, string(out)
}

// readyRepo has v1.0.0 tagged 48 h ago and v1.0.1 bumped after it, with no
// update-path change: every check passes.
func readyRepo(t *testing.T) *readinessRepo {
	r := newReadinessRepo(t)
	r.setVersion(60, "1.0.0")
	r.tag(48, "v1.0.0")
	r.commit(30, "internal/ui/view.go", "package ui\n", "feat(ui): a view")
	r.setVersion(20, "1.0.1")
	return r
}

func requireExit(t *testing.T, code int, out string, want int, contains ...string) {
	t.Helper()
	if code != want {
		t.Fatalf("exit %d, want %d\n%s", code, want, out)
	}
	for _, c := range contains {
		if !strings.Contains(out, c) {
			t.Fatalf("output lacks %q:\n%s", c, out)
		}
	}
}

func TestReleaseReadiness_ReadyCandidatePasses(t *testing.T) {
	r := readyRepo(t)
	code, out := r.run()
	requireExit(t, code, out, 0, "READY: tag v1.0.1", "none since v1.0.0")

	// Past 24 h the gap rule does not apply, so a --fixes naming another
	// tag is not a reason to refuse.
	code, out = r.run("--fixes", "v0.9.0")
	requireExit(t, code, out, 0, "--fixes v0.9.0 not needed", "READY: tag v1.0.1")
}

// The gap and version checks see every v* tag, not only the ones on HEAD's
// history: a hotfix tagged on a release branch 5 h ago still counts.
func TestReleaseReadiness_SeesTagsOnOtherBranches(t *testing.T) {
	r := newReadinessRepo(t)
	r.setVersion(60, "1.0.0")
	r.tag(48, "v1.0.0")
	r.git(40, "checkout", "-q", "-b", "release-1.0")
	r.setVersion(6, "1.0.1")
	r.tag(5, "v1.0.1")
	r.git(5, "checkout", "-q", "main")
	r.setVersion(4, "1.0.1")

	code, out := r.run()
	requireExit(t, code, out, 1,
		"last tag v1.0.1 is 5h old (< 24 h)",
		"Version 1.0.1 in cmd/agent-deck/main.go is not above v1.0.1",
		"NOT READY: 2 problem(s)")
}

// Without the remote's tags the gap check could miss one, so a failed
// fetch refuses; a working remote does not.
func TestReleaseReadiness_RefusesWhenTagFetchFails(t *testing.T) {
	r := readyRepo(t)
	r.git(0, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	code, out := r.run()
	requireExit(t, code, out, 1, "git fetch --tags origin failed", "NOT READY: 1 problem(s)")

	r.git(0, "remote", "set-url", "origin", r.dir)
	code, out = r.run()
	requireExit(t, code, out, 0, "READY: tag v1.0.1")
}

// RELEASE_READINESS_RUNNER is one executable; a path with a space in it
// still runs.
func TestReleaseReadiness_RunnerPathWithSpaces(t *testing.T) {
	r := readyRepo(t)
	dir := filepath.Join(t.TempDir(), "my tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "runner.sh")
	body := "#!/bin/sh\necho '--- PASS: TestReleaseReadiness_UpdatePath (1.00s)'\necho 'ok  \tx\t1.0s'\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	r.runner = p
	code, out := r.run()
	requireExit(t, code, out, 0, "READY: tag v1.0.1")
}

func TestReleaseReadiness_RefusesTagWithin24hUnlessItFixesIt(t *testing.T) {
	r := newReadinessRepo(t)
	r.setVersion(10, "1.0.0")
	r.tag(5, "v1.0.0")
	r.setVersion(1, "1.0.1")

	code, out := r.run()
	requireExit(t, code, out, 1, "last tag v1.0.0 is 5h old (< 24 h)", "NOT READY: 1 problem(s)")

	code, out = r.run("--fixes", "v1.0.0")
	requireExit(t, code, out, 0, "allowed: this release fixes v1.0.0", "READY: tag v1.0.1")

	code, out = r.run("--fixes", "v0.9.0")
	requireExit(t, code, out, 1, "--fixes names v0.9.0 but the last tag is v1.0.0")
}

// A candidate already tagged locally (not pushed yet) is checked against
// the tag before it, not against its own new tag.
func TestReleaseReadiness_TaggedHeadComparesWithPreviousTag(t *testing.T) {
	r := readyRepo(t)
	r.tag(0, "v1.0.1")
	code, out := r.run()
	requireExit(t, code, out, 0, "HEAD is already tagged v1.0.1", "last tag v1.0.0", "READY: tag v1.0.1")
}

func TestReleaseReadiness_RefusesUnbumpedVersion(t *testing.T) {
	r := newReadinessRepo(t)
	r.setVersion(60, "1.0.0")
	r.tag(48, "v1.0.0")
	r.commit(30, "internal/ui/view.go", "package ui\n", "feat(ui): a view")

	code, out := r.run()
	requireExit(t, code, out, 1, "Version 1.0.0 in cmd/agent-deck/main.go is not above v1.0.0")
}

func TestReleaseReadiness_RefusesUnsoakedUpdateChangeUnlessOwnerOK(t *testing.T) {
	r := readyRepo(t)
	r.commit(15, "internal/update/old.go", "package update\n", "fix(update): soaked change")
	r.commit(3, "internal/update/swap.go", "package update\n", "fix(update): swap the binary atomically")
	r.commit(2, "internal/update/swap_test.go", "package update\n", "test(update): only a test")

	code, out := r.run()
	requireExit(t, code, out, 1,
		"fix(update): swap the binary atomically  <- soaked 3h",
		"(15h ago) fix(update): soaked change",
		"1 update/auto-install/deploy change(s) merged less than 12 h ago")
	if strings.Contains(out, "only a test") {
		t.Fatalf("a test-only commit was listed as an update change:\n%s", out)
	}

	code, out = r.run("--owner-ok")
	requireExit(t, code, out, 0, "allowed: 1 change(s) under 12 h, the owner agreed", "READY: tag v1.0.1")
}

// The soak covers the files past update bugs lived in (#2245, #2252,
// #2271): the TUI's auto-install trigger and nudge, the binary watcher and
// self-restart, home.go, the ssh/remote deploy and the child environment.
func TestReleaseReadiness_SoakCoversTheTUIAndRemoteUpdatePaths(t *testing.T) {
	for _, path := range []string{
		"internal/ui/update_auto.go",
		"internal/ui/update_install.go",
		"internal/ui/update_nudge.go",
		"internal/ui/binary_watch.go",
		"internal/ui/restart.go",
		"internal/ui/home.go",
		"internal/session/ssh.go",
		"cmd/agent-deck/remote_cmd.go",
		"internal/childenv/childenv.go",
	} {
		t.Run(path, func(t *testing.T) {
			r := readyRepo(t)
			r.commit(2, path, "package x\n", "fix: change "+path)
			code, out := r.run()
			requireExit(t, code, out, 1, "fix: change "+path+"  <- soaked 2h", "1 update/auto-install/deploy change(s)")
		})
	}
	// Their tests are not update changes.
	r := readyRepo(t)
	r.commit(2, "internal/ui/update_auto_test.go", "package ui\n", "test(ui): only a test")
	code, out := r.run()
	requireExit(t, code, out, 0, "READY: tag v1.0.1")
}

// Soak age is when the change reached the branch: a commit dated 30 h ago
// that sits on a commit made 3 h ago landed at most 3 h ago.
func TestReleaseReadiness_SoakAgeIsWhenTheChangeReachedTheBranch(t *testing.T) {
	r := readyRepo(t)
	r.commit(3, "internal/ui/view2.go", "package ui\n", "feat(ui): another view")
	r.commit(30, "internal/update/swap.go", "package update\n", "fix(update): old commit pushed late")

	code, out := r.run()
	requireExit(t, code, out, 1, "(3h ago) (commit dated ", "fix(update): old commit pushed late  <- soaked 3h")
}

func TestReleaseReadiness_RequiresTheUpdatePathTestToPass(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		code      int
	}{
		{"failed", "--- FAIL: TestReleaseReadiness_UpdatePath (1.00s)\nFAIL", 1},
		{"skipped", "--- SKIP: TestReleaseReadiness_UpdatePath (0.00s)\nok  \tx\t0.1s", 0},
		{"not run", "ok  \tx\t0.1s [no tests to run]", 0},
		{"pass line but runner failed", "--- PASS: TestReleaseReadiness_UpdatePath (1.00s)\nFAIL", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := readyRepo(t)
			r.runner = r.stubRunner(tc.out, tc.code)
			code, out := r.run()
			requireExit(t, code, out, 1, "TestReleaseReadiness_UpdatePath did not pass")
		})
	}
}

// An untracked file is refused like a modified one: the end-to-end test
// builds the whole checkout, so it would test code the tag does not hold.
func TestReleaseReadiness_RefusesUntrackedFiles(t *testing.T) {
	r := readyRepo(t)
	if err := os.WriteFile(filepath.Join(r.dir, "cmd", "agent-deck", "extra.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := r.run()
	requireExit(t, code, out, 1, "the checkout has uncommitted or untracked files")
}

// Without RELEASE_READINESS_RUNNER and without deck-test on PATH the script
// runs `go test` itself, in the checkout root. A fake go on PATH records
// where it ran and with which arguments.
func TestReleaseReadiness_DefaultRunnerIsGoTestInTheCheckout(t *testing.T) {
	r := readyRepo(t)
	r.runner = ""
	shim := t.TempDir()
	record := filepath.Join(t.TempDir(), "go-call")
	goShim := fmt.Sprintf("#!/bin/sh\n{ pwd; echo \"$@\"; } > '%s'\necho '--- PASS: TestReleaseReadiness_UpdatePath (1.00s)'\necho 'ok  \tx\t1.0s'\n", record)
	if err := os.WriteFile(filepath.Join(shim, "go"), []byte(goShim), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := []string{shim}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "deck-test")); err == nil {
			continue // keep deck-test out of reach
		}
		dirs = append(dirs, d)
	}
	r.path = strings.Join(dirs, string(os.PathListSeparator))

	code, out := r.run()
	requireExit(t, code, out, 0, "READY: tag v1.0.1")
	call, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the script did not run go: %v\n%s", err, out)
	}
	lines := strings.SplitN(strings.TrimSpace(string(call)), "\n", 2)
	wantDir, err := filepath.EvalSymlinks(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir, _ := filepath.EvalSymlinks(lines[0]); gotDir != wantDir {
		t.Fatalf("go ran in %q, want the checkout %q", lines[0], wantDir)
	}
	wantArgs := "test -count=1 -v -run ^TestReleaseReadiness_UpdatePath$ ./internal/releasetests/"
	if len(lines) < 2 || lines[1] != wantArgs {
		t.Fatalf("go called with %q, want %q", lines[1:], wantArgs)
	}
}
