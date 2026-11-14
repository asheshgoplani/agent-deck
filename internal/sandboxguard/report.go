package sandboxguard

import (
	"path/filepath"
	"sync"
	"testing"
)

// reporter is a test that asked, through Report, to fail instead of letting a
// guard violation panic the whole test binary.
type reporter struct {
	t   testing.TB
	dir string // quarantine root: refused paths are rebased under it
}

var (
	reportersMu sync.Mutex
	reporters   []*reporter
)

// reportEnv is set, through t.Setenv, by every test that calls Report. Its
// only job is the t.Setenv guard: the testing package refuses t.Setenv in a
// test that runs, or later asks to run, under t.Parallel.
const reportEnv = "AGENT_DECK_SANDBOXGUARD_REPORT"

// Report makes a guard violation in Must, MustHome or HomeJoin fail t (with
// t.Errorf) instead of panicking, for as long as t runs. The default stays a
// panic: it stops the test before anything reads or writes the live file, but
// it aborts the whole test binary, so every other test's result in the package
// is lost. With Report, the violating test FAILs and the rest still run and
// report.
//
// The resolver that tripped the guard has no testing.TB and may run on any
// goroutine, so it cannot stop the test (runtime.Goexit off the test goroutine
// would end the binary). Instead it gets back the refused path rebased under a
// t.TempDir() quarantine root ("/home/u/.claude" becomes
// "<tmp>/home/u/.claude"), which holds nothing to read and keeps any write
// away from the real home.
//
// The guard cannot map a goroutine to its test, so it blames the test that
// called Report. That is only right when no other test runs at the same time,
// so Report is for serial tests: it calls t.Setenv, and the testing package
// panics when such a test is, or becomes, parallel. That check covers only
// the test that called Report: its subtests may still call t.Parallel, and
// while they run the parent stays registered, so a violation in a parallel
// subtest is charged to the parent (the parent FAILs, not the subtest). Call
// Report from the helper that already sets HOME with t.Setenv. When no test is
// registered, a violation panics as before.
func Report(t testing.TB) {
	t.Helper()
	t.Setenv(reportEnv, t.Name())
	r := &reporter{t: t, dir: t.TempDir()}
	reportersMu.Lock()
	reporters = append(reporters, r)
	reportersMu.Unlock()
	t.Cleanup(func() {
		reportersMu.Lock()
		defer reportersMu.Unlock()
		for i, o := range reporters {
			if o == r {
				reporters = append(reporters[:i:i], reporters[i+1:]...)
				return
			}
		}
	})
}

// refuse fails the test that most recently called Report with err and returns
// path rebased under its quarantine root. Reporting tests are serial, so the
// last one registered is the running test (an enclosing test that also called
// Report fails with it). With no test registered it panics, the default.
func refuse(err error, path string) string {
	reportersMu.Lock()
	var r *reporter
	if n := len(reporters); n > 0 {
		r = reporters[n-1]
	}
	reportersMu.Unlock()
	if r == nil {
		panic(err.Error())
	}
	r.t.Errorf("%s", err)
	return filepath.Join(r.dir, path)
}
