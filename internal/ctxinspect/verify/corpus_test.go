package verify

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxfixture"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/registry"
)

// managedClaudePolicyPaths are the machine-wide policy files Claude Code loads
// before anything else. They belong to the host, not to a fixture, so a case
// that reconstructs a memory hierarchy cannot be asserted on a machine that has
// one — the report would correctly contain a file the fixture never declared.
var managedClaudePolicyPaths = []string{
	"/Library/Application Support/ClaudeCode/CLAUDE.md",
	"/etc/claude-code/CLAUDE.md",
}

// hostHasManagedClaudePolicy reports whether this machine carries a managed
// policy file.
func hostHasManagedClaudePolicy() (string, bool) {
	for _, p := range managedClaudePolicyPaths {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// isolateCorpusEnv neutralizes the environment variables that would change what
// a fixture loads. They are cleared rather than assumed absent: a developer with
// CLAUDE_CODE_DISABLE_CLAUDE_MDS set would otherwise see this suite fail for a
// reason that has nothing to do with the code.
func isolateCorpusEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CODE_DISABLE_CLAUDE_MDS", "")
	t.Setenv("AGENTDECK_CONTEXT_WINDOW", "")
}

// inspectCase materializes a fixture into a temporary directory and inspects it.
func inspectCase(t *testing.T, c *ctxfixture.Case) *ctxinspect.Report {
	t.Helper()
	isolateCorpusEnv(t)
	if c.Adapter == "claude" {
		if path, ok := hostHasManagedClaudePolicy(); ok {
			t.Skipf("this machine has a managed Claude policy file at %s, which the fixture's memory hierarchy cannot account for", path)
		}
	}

	req, _, err := c.MaterializeFixed(fixtureParent(t))
	if err != nil {
		if errors.Is(err, ctxfixture.ErrParentTooLong) {
			t.Skipf("this machine's temporary directory leaves no room for a fixed-length fixture root: %v", err)
		}
		t.Fatalf("materializing %s: %v", c.Name, err)
	}
	rep, err := registry.Default().Inspect(context.Background(), req)
	if err != nil {
		t.Fatalf("inspecting %s: %v", c.Name, err)
	}
	return rep
}

// fixtureParent returns a short-lived parent directory for a fixture root.
//
// It is deliberately not t.TempDir(): Go derives that path from the test's
// name, so a long subtest name would leave no room to pad the root to
// [ctxfixture.FixedRootLength] and the case would skip instead of assert.
func fixtureParent(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctxfix")
	if err != nil {
		t.Fatalf("creating a fixture parent directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestCorpusReconciles is the plumbing check that has to hold for every case:
// the attributed costs plus the unattributed remainder equal the measured
// anchor exactly, and the remainder is never negative.
//
// The residual is defined by subtraction, so equality is not a discovery — it
// is an assertion that the definition is actually what the code implements, and
// that nothing double-counts a rollup or leaks a potential cost into a total.
// The non-negativity is the real finding: a negative remainder means we
// attributed more than the provider measured.
func TestCorpusReconciles(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the fixture corpus is empty: this suite would pass without checking anything")
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			rep := inspectCase(t, c)

			if err := rep.Validate(); err != nil {
				t.Fatalf("the report contradicts itself: %v", err)
			}

			if !c.Expect.Anchored {
				if rep.Anchor != nil {
					v, _ := rep.Anchor.Tokens.Value()
					t.Fatalf("an anchor of %d was claimed for a case that cannot be anchored", v)
				}
				if rep.Reconciliation.Status != ctxinspect.ReconNoAnchor {
					t.Fatalf("reconciliation = %s, want no-anchor", rep.Reconciliation.Status)
				}
				if _, ok := rep.Reconciliation.Unaccounted.Value(); ok {
					t.Fatal("with no anchor the remainder must be unknown, not a number")
				}
				return
			}

			if rep.Anchor == nil {
				t.Fatal("no anchor was claimed for a cold-start case")
			}
			anchor, ok := rep.Anchor.Tokens.Value()
			if !ok {
				t.Fatal("the anchor carries no value")
			}
			if anchor != c.Expect.AnchorTokens {
				t.Fatalf("anchor = %d, want %d (derived by hand from the fixture's own usage record)", anchor, c.Expect.AnchorTokens)
			}

			attributed, complete := rep.AttributedTotal()
			if !complete {
				t.Fatal("a fixture case must price every item, or the reconciliation below proves nothing")
			}
			residual, ok := rep.Reconciliation.Unaccounted.Value()
			if !ok {
				t.Fatal("the remainder must be a number when an anchor exists and everything is priced")
			}
			if residual < 0 {
				t.Fatalf("RECONCILIATION FAILED: attributed %d exceeds the measured %d by %d", attributed, anchor, -residual)
			}
			if attributed+residual != anchor {
				t.Fatalf("Σattributed(%d) + unaccounted(%d) = %d, want the anchor %d exactly",
					attributed, residual, attributed+residual, anchor)
			}

			fixed, fixedComplete := rep.FixedTotal()
			if !fixedComplete || fixed != anchor {
				t.Fatalf("fixed total = %d (complete=%v), want the anchor %d: the gauge must equal what the provider measured",
					fixed, fixedComplete, anchor)
			}
			if rep.Reconciliation.Status != ctxinspect.ReconOK {
				t.Fatalf("reconciliation = %s, want ok", rep.Reconciliation.Status)
			}
		})
	}
}

// TestCorpusEstimatorHasNotDrifted is the alarm that keeps a characters-per-token
// heuristic from quietly rotting.
//
// Each case records the attributed sum a known-good run produced. The fixture's
// bytes never change and its anchor is fixed, so any movement here is a change
// in the estimator — which also moves the share of the measured total that
// attribution explains, and therefore what the panel tells the user is worth
// deleting. CI fails rather than the panel silently re-slicing itself.
func TestCorpusEstimatorHasNotDrifted(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}

	checked := 0
	for _, c := range cases {
		if c.Expect.AttributedTokens <= 0 || c.Expect.EstimateTolerancePct <= 0 {
			continue
		}
		checked++
		t.Run(c.Name, func(t *testing.T) {
			rep := inspectCase(t, c)
			attributed, complete := rep.AttributedTotal()
			if !complete {
				t.Fatal("an incomplete attributed total cannot be compared against a recorded one")
			}
			want := c.Expect.AttributedTokens
			errPct := float64(attributed-want) / float64(want) * 100
			if math.Abs(errPct) > c.Expect.EstimateTolerancePct {
				t.Fatalf("attributed = %d, recorded %d (%+.1f%%), outside the declared ±%.1f%% band: the estimator has drifted, or the fixture changed",
					attributed, want, errPct, c.Expect.EstimateTolerancePct)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no case declares a recorded attributed total: the estimator-drift alarm is disarmed")
	}
}

// TestCorpusDegradesHonestly checks the negative cases: a resumed session and a
// transcript this build does not understand must both say what went wrong
// rather than silently reporting less.
func TestCorpusDegradesHonestly(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}

	for _, c := range cases {
		if len(c.Expect.Caveats) == 0 {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			rep := inspectCase(t, c)
			got := make(map[string]bool, len(rep.Caveats))
			for _, cv := range rep.Caveats {
				got[cv.Code] = true
			}
			for _, want := range c.Expect.Caveats {
				if !got[want] {
					t.Errorf("the report is missing the caveat %q; it carries %v", want, caveatCodes(rep))
				}
			}
			if len(rep.Categories) < c.Expect.MinCategories {
				t.Errorf("%d categories, want at least %d: a degraded report must still describe what it could not read",
					len(rep.Categories), c.Expect.MinCategories)
			}
		})
	}
}

// TestCorpusAdapterRouting checks that each case is claimed by the adapter it
// declares, so a routing change cannot silently downgrade a case to the generic
// inventory and still pass every assertion above.
func TestCorpusAdapterRouting(t *testing.T) {
	cases, err := ctxfixture.LoadAll()
	if err != nil {
		t.Fatalf("loading the fixture corpus: %v", err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			rep := inspectCase(t, c)
			if rep.Adapter != c.Adapter {
				t.Fatalf("adapter = %q, want %q", rep.Adapter, c.Adapter)
			}
			if _, ok := SpecForAdapter(rep.Adapter); !ok {
				t.Fatalf("adapter %q has no verification spec, so its report can never be checked against the harness", rep.Adapter)
			}
		})
	}
}

// caveatCodes lists a report's caveat codes for a failure message.
func caveatCodes(rep *ctxinspect.Report) []string {
	out := make([]string, 0, len(rep.Caveats))
	for _, c := range rep.Caveats {
		out = append(out, c.Code)
	}
	return out
}
