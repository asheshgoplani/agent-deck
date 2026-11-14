package sandboxguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// reportChildEnv makes the TestReportChild_* tests run: they are the inner
// tests of the re-exec'd binaries below and skip in a normal run.
const reportChildEnv = "SANDBOXGUARD_REPORT_CHILD"

// refusedPath returns a path the guard refuses in this binary, or skips.
func refusedPath(t *testing.T) string {
	t.Helper()
	homes := RealHomes()
	if len(homes) == 0 {
		t.Skip("no guardable home in this environment")
	}
	p := filepath.Join(homes[0], ".sandboxguard-report-probe")
	if Violation(p) == "" {
		t.Skipf("%s is not refused in this environment", p)
	}
	return p
}

func runReportChild(t *testing.T, run string) (string, error) {
	t.Helper()
	refusedPath(t)
	cmd := exec.Command(os.Args[0], "-test.run", run, "-test.v", "-test.count=1", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), reportChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func wantLines(t *testing.T, out string, re ...string) {
	t.Helper()
	for _, r := range re {
		if !regexp.MustCompile(`(?m)` + r).MatchString(out) {
			t.Errorf("child output has no line matching %q", r)
		}
	}
}

// TestReport_ViolationFailsOnlyThatTest pins #2801: with Report(t), a guard
// violation fails the test it fired in, on or off the test goroutine, and the
// tests after it still run and report. Before Report, the first violation
// panicked and the binary died, so the later tests never reported.
func TestReport_ViolationFailsOnlyThatTest(t *testing.T) {
	out, err := runReportChild(t, `^TestReportChild_Serial`)
	if err == nil {
		t.Errorf("child exited 0, want a failure from the violating tests")
	}
	if strings.Contains(out, "panic:") {
		t.Errorf("child panicked; a guard violation under Report must fail one test, not abort the binary")
	}
	wantLines(t, out,
		`^--- FAIL: TestReportChild_Serial1_Violates `,
		`^--- PASS: TestReportChild_Serial2_StillRuns `,
		`^--- FAIL: TestReportChild_Serial3_ViolatesOffGoroutine `,
		`^--- PASS: TestReportChild_Serial4_StillRuns `,
		`sandboxguard: report probe resolved to `,
	)
	if t.Failed() {
		t.Logf("child output:\n%s", out)
	}
}

// TestReport_RefusesParallelTests pins the t.Parallel guard: the guard
// blames the test that called Report, which is only right when no other test
// runs beside it, so Report in a parallel test must stop loudly instead of
// misattributing a violation.
func TestReport_RefusesParallelTests(t *testing.T) {
	out, err := runReportChild(t, `^TestReportChild_ParallelCaller$`)
	if err == nil || !regexp.MustCompile(`panic: testing: .*t\.Parallel`).MatchString(out) {
		t.Fatalf("child err=%v, want the testing package's t.Setenv/t.Parallel panic; output:\n%s", err, out)
	}
}

// TestReport_DefaultStillPanics pins that a test which did not call Report
// keeps the default: the violation panics.
func TestReport_DefaultStillPanics(t *testing.T) {
	out, err := runReportChild(t, `^TestReportChild_NoReporter$`)
	if err == nil || !strings.Contains(out, "panic: sandboxguard: report probe resolved to ") {
		t.Fatalf("child err=%v, want a sandboxguard panic; output:\n%s", err, out)
	}
}

func skipUnlessReportChild(t *testing.T) {
	t.Helper()
	if os.Getenv(reportChildEnv) != "1" {
		t.Skip("inner test of the TestReport_* re-exec")
	}
}

func TestReportChild_Serial1_Violates(t *testing.T) {
	skipUnlessReportChild(t)
	Report(t)
	p := refusedPath(t)
	got := Must("report probe", p)
	if got == p || Violation(got) != "" {
		t.Errorf("Must returned %q, want a quarantined path outside the real home", got)
	}
}

func TestReportChild_Serial2_StillRuns(t *testing.T) {
	skipUnlessReportChild(t)
}

func TestReportChild_Serial3_ViolatesOffGoroutine(t *testing.T) {
	skipUnlessReportChild(t)
	Report(t)
	p := refusedPath(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Must("report probe", p)
	}()
	wg.Wait()
}

func TestReportChild_Serial4_StillRuns(t *testing.T) {
	skipUnlessReportChild(t)
}

func TestReportChild_ParallelCaller(t *testing.T) {
	skipUnlessReportChild(t)
	t.Parallel()
	Report(t)
}

func TestReportChild_NoReporter(t *testing.T) {
	skipUnlessReportChild(t)
	Must("report probe", refusedPath(t))
}
