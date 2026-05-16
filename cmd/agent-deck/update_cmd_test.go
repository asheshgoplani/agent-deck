package main

import (
	"strings"
	"testing"
)

// fakeBrewRunner records calls and returns canned outputs/errors per call,
// so tests can simulate brew's stdout/stderr without touching the real binary.
type fakeBrewRunner struct {
	calls   [][]string
	outputs []string
	errs    []error
}

func (f *fakeBrewRunner) Run(args ...string) ([]byte, error) {
	n := len(f.calls)
	f.calls = append(f.calls, args)
	var out []byte
	if n < len(f.outputs) {
		out = []byte(f.outputs[n])
	}
	var err error
	if n < len(f.errs) {
		err = f.errs[n]
	}
	return out, err
}

// TestUpdate_DetectsNoVersionBump_FailsLoudly_RegressionFor954 reproduces #954
// (reported by @alexandergharibian): `agent-deck update` printed
//
//	Already up-to-date. Warning: 1.8.3 already installed
//	✓ Updated to v1.9.4
//
// even though brew explicitly refused to upgrade. The CLI must instead fail
// loudly with a clear message — never claim success when brew said no.
func TestUpdate_DetectsNoVersionBump_FailsLoudly_RegressionFor954(t *testing.T) {
	runner := &fakeBrewRunner{
		outputs: []string{
			// `brew update` — metadata fetch succeeds.
			"Already up-to-date.\n",
			// `brew upgrade asheshgoplani/tap/agent-deck` — brew refuses
			// because the tap formula still pins the old version. Exit 0.
			"Warning: agent-deck 1.8.3 already installed\n",
		},
		errs: []error{nil, nil},
	}

	err := runHomebrewUpgradeWith(runner, "brew upgrade asheshgoplani/tap/agent-deck")
	if err == nil {
		t.Fatalf("expected error when brew refuses upgrade (regression #954: CLI printed '✓ Updated' while brew said 'already installed')")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "did not upgrade") {
		t.Errorf("error must clearly state brew did not upgrade; got: %v", err)
	}
	if !strings.Contains(msg, "954") {
		t.Errorf("error should cite issue #954 so users can trace; got: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 brew calls (update + upgrade); got %d: %+v", len(runner.calls), runner.calls)
	}
	if len(runner.calls[0]) == 0 || runner.calls[0][0] != "update" {
		t.Errorf("first call should be `brew update`; got %v", runner.calls[0])
	}
	if len(runner.calls[1]) == 0 || runner.calls[1][0] != "upgrade" {
		t.Errorf("second call should be `brew upgrade`; got %v", runner.calls[1])
	}
}

// TestUpdate_AcceptsRealUpgrade_NoFalseFailure guards against over-shooting the
// #954 fix: when brew actually upgrades, runHomebrewUpgradeWith must return nil.
func TestUpdate_AcceptsRealUpgrade_NoFalseFailure(t *testing.T) {
	runner := &fakeBrewRunner{
		outputs: []string{
			"Already up-to-date.\n",
			"==> Upgrading asheshgoplani/tap/agent-deck\n==> Pouring agent-deck-1.9.4.bottle.tar.gz\n🍺  /opt/homebrew/Cellar/agent-deck/1.9.4: 5 files\n",
		},
		errs: []error{nil, nil},
	}
	if err := runHomebrewUpgradeWith(runner, "brew upgrade asheshgoplani/tap/agent-deck"); err != nil {
		t.Fatalf("real-upgrade output should not be flagged as a refused upgrade; got: %v", err)
	}
}
